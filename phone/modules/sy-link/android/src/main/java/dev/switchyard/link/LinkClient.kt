package dev.switchyard.link

import android.util.Log
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/** What the client tells its owner (the service). */
interface LinkEvents {
  /** The connection state in Store changed. */
  fun state()

  /** The link must stop: revoked (4403), upgrade (4426), a changed hub key or repeated refusals. */
  fun stopped(status: String, why: String)

  /** A retry timer longer than 10 s was set (push.md 14.4 "Doze": the service may back it with an exact alarm). */
  fun longTimer(atMs: Long)
}

/**
 * The link of link.md over OkHttp: WebSocket with subprotocol `sy-link.1`, the challenge login,
 * frames through [FrameProcessor], batched acks, actions, and the backoff of 14.4; long-poll
 * (link.md 2.1) after 3 failed handshakes while the hub's `/health` answers. Every state change
 * runs on one thread (`exec`); OkHttp's callbacks are posted to it.
 */
class LinkClient(
  private val store: Store,
  private val signer: Signer,
  private val processor: FrameProcessor,
  private val events: LinkEvents,
  private val client: () -> JSONObject,
  private val wake: (() -> Unit) -> Unit,
) : ActionTransport {
  private val exec = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "sy-link").apply { isDaemon = true } }
  private val backoff = Backoff()
  private val gen = AtomicInteger(0)

  // all below: exec thread only
  private var running = false
  private var ws: WebSocket? = null
  private var phase = Phase.IDLE
  private var nonce: String? = null
  private var opened = false
  private var connectedAt = 0L
  private var failedHandshakes = 0
  private var urlIndex = 0
  private var timer: ScheduledFuture<*>? = null
  private var ackTimer: ScheduledFuture<*>? = null
  private var pendingAck = 0L
  private var unacked = 0
  private var lastCode = 0
  private var codeStreak = 0
  private var pollThread: Thread? = null
  private var pollUntil = 0L
  private var drainingRetryMs: Long? = null

  private val waiters = ConcurrentHashMap<String, Pair<CountDownLatch, IntArray>>()

  private enum class Phase { IDLE, CHALLENGE, WELCOME, READY }

  val connected get() = store.status == "connected"

  fun start() = exec.execute {
    if (running) return@execute
    running = true
    backoff.reset()
    connect()
  }

  /**
   * Stops the link and reports "off", unless the link already stopped itself for good (4403,
   * 4426, a changed hub key): that status says why alerts stopped, and the service shutting the
   * client down right after [LinkEvents.stopped] (onDestroy) must not overwrite it. The app's
   * unpair reads "revoked" to leave the "removed from the hub" notice (CloseCodes.noticeAfterReset).
   */
  fun stop(): Future<*> = exec.submit {
    running = false
    drop()
    if (store.status !in TERMINAL) setState("off", null, null)
  }

  /** "Reconnect now" and a network change: drop the socket, reset the backoff, connect at once. */
  fun reconnectNow() = exec.execute {
    if (!running) return@execute
    backoff.reset()
    drop()
    connect()
  }

  private fun drop() {
    cancelTimer()
    closeSocket(1000)
    stopPoll()
  }

  /** An exact alarm fired: connect now if a retry timer was waiting. */
  fun retryIfWaiting() = exec.execute {
    if (running && phase == Phase.IDLE && timer != null) {
      cancelTimer()
      connect()
    }
  }

  fun shutdown() {
    stop()
    exec.shutdown()
  }

  // ------------------------------------------------------------------ connect

  /** The WebSocket's client: derived from [HTTP], so a reconnect makes no new pool or threads. */
  internal fun wsHttp(pingS: Long = store.pingS): OkHttpClient =
    HTTP.newBuilder().pingInterval(pingS, TimeUnit.SECONDS).readTimeout(0, TimeUnit.MILLISECONDS).build()

  private fun connect() {
    if (!running) return
    val urls = store.urls
    if (urls.isEmpty() || store.device == null) {
      setState("error", null, "not_configured")
      return
    }
    if (pollUntil > System.currentTimeMillis()) return startPoll(urls)
    val url = urls[urlIndex % urls.size]
    val my = gen.incrementAndGet()
    phase = Phase.CHALLENGE
    opened = false
    nonce = null
    setState("connecting", "ws", store.lastError)
    val req = Request.Builder().url(url).header("Sec-WebSocket-Protocol", SUBPROTOCOL).build()
    ws = wsHttp().newWebSocket(req, object : WebSocketListener() {
      override fun onOpen(webSocket: WebSocket, response: Response) = post(my) { opened = true }
      override fun onMessage(webSocket: WebSocket, text: String) = post(my) { onMessage(text) }
      override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
        webSocket.close(code, null)
        post(my) { closed(code, null) }
      }
      override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = post(my) { closed(code, null) }
      override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) =
        post(my) { closed(0, if (response != null) "http_${response.code}" else t.javaClass.simpleName) }
    })
  }

  /** Runs `f` on exec if the connection `my` is still the current one. */
  private fun post(my: Int, f: () -> Unit) {
    try {
      exec.execute { if (my == gen.get()) f() }
    } catch (_: Exception) {
      // shut down
    }
  }

  private fun closeSocket(code: Int) {
    flushAck() // processed frames are acked before a clean close (hello.last covers the rest)
    gen.incrementAndGet()
    ws?.close(code, null)
    ws = null
    phase = Phase.IDLE
  }

  private fun onMessage(text: String) {
    when (phase) {
      Phase.CHALLENGE -> {
        val o = try { JSONObject(text) } catch (_: Exception) { return closeWith(4400, "bad_frame") }
        val pin = store.hubFp ?: return closeWith(1000, "not_configured")
        val pub = store.hubPub ?: return closeWith(1000, "not_configured")
        val verifier = TinkHubVerifier(pub)
        val c = Login.checkChallenge(o, pin, verifier)
        if (c == null) {
          // Never sign a nonce for a hub we do not trust (link.md 3.1); another key is 14.7's "Hub key changed".
          if (Login.fromAnotherHub(o, pin, verifier)) return hubKeyChanged()
          return closeWith(4400, "bad_frame")
        }
        failedHandshakes = 0
        nonce = c.nonce
        val device = store.device ?: return
        ws?.send(Login.hello(c, device, store.last, client(), signer).toString())
        phase = Phase.WELCOME
      }
      Phase.WELCOME, Phase.READY -> handleFrame(text, nonce ?: return)
      Phase.IDLE -> Unit
    }
  }

  private fun handleFrame(text: String, nonce: String) {
    var out: Outcome? = null
    wake { out = processor.onText(text, nonce) }
    val o = out ?: return
    o.note?.let { if (o.reject != null || it == "bad signature") Log.w(TAG, "frame dropped: $it") }
    o.reject?.let { ws?.send(JSONObject().put("t", "reject").put("seq", it).toString()) }
    o.ack?.let { queueAck(it) }
    val (t, body) = o.ephemeral ?: return
    when (t) {
      "welcome" -> welcome(body)
      "action_result" -> waiters.remove(body.optString("id"))?.let { (latch, box) ->
        box[0] = body.optInt("status", 0)
        latch.countDown()
      }
      "notice" -> if (body.optString("code") == "draining") drainingRetryMs = 5_000L + (Math.random() * 10_000).toLong()
      else -> Unit
    }
  }

  private fun welcome(b: JSONObject) {
    if (phase != Phase.WELCOME) return
    phase = Phase.READY
    connectedAt = System.currentTimeMillis()
    codeStreak = 0
    b.optJSONArray("urls")?.let { a -> List(a.length()) { a.optString(it) }.filter(::isLinkUrl) }
      ?.takeIf { it.isNotEmpty() }?.let { store.setUrls(it) }
    setState("connected", "ws", null, connectedSince = connectedAt)
    // queued actions go out now (14.6 step 3), off this thread: they wait for answers on it
    Thread({ flushPending() }, "sy-link-actions").start()
  }

  private fun hubKeyChanged() {
    store.setVerified("changed")
    closeWith(1000, "hub_key_changed")
    stopAll("error", "Hub key changed: pair this phone again")
  }

  private fun closeWith(code: Int, why: String) {
    store.setError(why)
    closeSocket(code)
    if (running) scheduleRetry(backoff.next())
  }

  private fun closed(code: Int, error: String?) {
    val wasOpen = opened
    val lasted = if (connectedAt > 0) System.currentTimeMillis() - connectedAt else 0
    connectedAt = 0
    closeSocket(1000)
    if (!running) return
    backoff.connectionLasted(lasted)
    codeStreak = if (code == lastCode) codeStreak + 1 else 1
    lastCode = code
    val err = error ?: if (code == 0) "network" else "$code ${CloseCodes.word(code)}"
    store.setError(err)
    if (code == 0 && !wasOpen) {
      failedHandshakes++
      urlIndex++
      if (failedHandshakes >= 3 && healthAnswers()) {
        pollUntil = System.currentTimeMillis() + POLL_WS_RETRY_MS
        failedHandshakes = 0
        return connect()
      }
    }
    drainingRetryMs?.let { drainingRetryMs = null; return scheduleRetry(it) }
    when (val a = CloseCodes.decide(code, codeStreak)) {
      is CloseAction.Stop -> stopAll(a.status, a.why)
      is CloseAction.After -> scheduleRetry(maxOf(a.ms, backoff.next()))
      CloseAction.Superseded -> scheduleRetry(SUPERSEDED_RETRY_MS) // only a safety net (link.md 8)
      CloseAction.Retry -> scheduleRetry(backoff.next())
    }
  }

  private fun stopAll(status: String, why: String) {
    running = false
    cancelTimer()
    stopPoll()
    setState(status, null, why)
    events.stopped(status, why)
  }

  private fun scheduleRetry(ms: Long) {
    cancelTimer()
    val at = System.currentTimeMillis() + ms
    setState("backoff", null, store.lastError, nextRetryAt = at)
    timer = exec.schedule({ timer = null; connect() }, ms, TimeUnit.MILLISECONDS)
    if (ms > 10_000) events.longTimer(at)
  }

  private fun cancelTimer() {
    timer?.cancel(false)
    timer = null
  }

  private fun setState(status: String, transport: String?, err: String?, connectedSince: Long? = null, nextRetryAt: Long? = null) {
    store.setConnection(status, transport, connectedSince, nextRetryAt, err)
    events.state()
  }

  // ------------------------------------------------------------------ acks (link.md 5.6)

  private fun queueAck(seq: Long) {
    pendingAck = maxOf(pendingAck, seq)
    unacked++
    if (unacked >= ACK_EVERY) return flushAck()
    if (ackTimer == null) ackTimer = exec.schedule({ ackTimer = null; flushAck() }, ACK_MS, TimeUnit.MILLISECONDS)
  }

  private fun flushAck() {
    ackTimer?.cancel(false)
    ackTimer = null
    if (pendingAck > 0) ws?.send(JSONObject().put("t", "ack").put("seq", pendingAck).toString())
    pendingAck = 0
    unacked = 0
  }

  // ------------------------------------------------------------------ actions (push.md 14.6)

  override fun viaLink(id: String, token: String, timeoutMs: Long): Int? {
    if (!connected) return null
    val latch = CountDownLatch(1)
    val box = IntArray(1)
    waiters[id] = latch to box
    var sent = false
    val done = CountDownLatch(1)
    exec.execute {
      sent = phase == Phase.READY && ws?.send(JSONObject().put("t", "action").put("id", id).put("token", token).toString()) == true
      done.countDown()
    }
    done.await(1, TimeUnit.SECONDS)
    if (!sent) {
      waiters.remove(id)
      return null
    }
    val ok = latch.await(timeoutMs, TimeUnit.MILLISECONDS)
    waiters.remove(id)
    return if (ok) box[0] else null
  }

  override fun viaHttp(origin: String, token: String, timeoutMs: Long): Int? = postAction(origin, token, timeoutMs)

  private fun flushPending() {
    try {
      ActionRunner(store, this, System::currentTimeMillis).flush()
    } catch (e: Exception) {
      Log.w(TAG, "pending actions: ${e.javaClass.simpleName}")
    }
  }

  // ------------------------------------------------------------------ long-poll (link.md 2.1)

  private fun healthAnswers(): Boolean {
    val origin = store.origin ?: return false
    return try {
      timed(1500).newCall(Request.Builder().url("$origin/health").build()).execute().use { it.code in 200..599 && !it.isRedirect }
    } catch (_: IOException) {
      false
    }
  }

  private fun startPoll(urls: List<String>) {
    val url = pollUrl(urls[urlIndex % urls.size])
    val my = gen.incrementAndGet()
    setState("connecting", "poll", store.lastError)
    val t = Thread({
      val p = Poller(store, signer, processor, client, wake)
      var err: String? = null
      try {
        p.loop(url, until = pollUntil, alive = { my == gen.get() && running }) {
          post(my) { setState("connected", "poll", null, connectedSince = connectedAt.takeIf { it > 0 } ?: System.currentTimeMillis().also { connectedAt = it }) }
        }
      } catch (e: Poller.Stop) {
        post(my) { if (e.status == "changed") hubKeyChanged() else stopAll(e.status, e.why) }
        return@Thread
      } catch (e: Exception) {
        err = e.javaClass.simpleName
      }
      post(my) {
        connectedAt = 0
        if (pollUntil <= System.currentTimeMillis()) pollUntil = 0 // 30 minutes: try the WebSocket again
        store.setError(err ?: store.lastError)
        if (running) scheduleRetry(if (err != null) backoff.next() else 0)
      }
    }, "sy-link-poll")
    pollThread = t
    t.start()
  }

  private fun stopPoll() {
    gen.incrementAndGet()
    pollThread?.interrupt()
    pollThread = null
  }

  /** One check with instant delivery off (push.md 14.9): log in over long-poll, process, ack, leave. */
  fun checkOnce(): Boolean {
    val urls = store.urls
    if (urls.isEmpty() || store.device == null) return false
    return try {
      Poller(store, signer, processor, client, wake).loop(pollUrl(urls[0]), rounds = 2, alive = { true }) {}
      true
    } catch (e: Poller.Stop) {
      events.stopped(if (e.status == "changed") "error" else e.status, e.why)
      false
    } catch (e: Exception) {
      store.setError(e.javaClass.simpleName)
      false
    }
  }

  companion object {
    const val TAG = "SyLink"

    /** The statuses [stopAll] leaves behind: only a start or the unpair's reset clears them. */
    val TERMINAL = setOf("revoked", "upgrade", "error")
    const val SUBPROTOCOL = "sy-link.1"
    const val ACK_MS = 500L
    const val ACK_EVERY = 16
    const val POLL_WS_RETRY_MS = 30 * 60_000L
    const val SUPERSEDED_RETRY_MS = 120_000L
    private val JSON = "application/json".toMediaType()

    /**
     * The process's one OkHttp client (push.md 14.4): every request derives from it with
     * newBuilder(), which shares its dispatcher, connection pool and their threads. A client
     * built per reconnect would start a new pool and dispatcher each time.
     */
    val HTTP: OkHttpClient by lazy {
      OkHttpClient.Builder().connectTimeout(15, TimeUnit.SECONDS).followRedirects(false).followSslRedirects(false).build()
    }

    /** [HTTP] with a whole-call timeout. */
    fun timed(ms: Long): OkHttpClient = HTTP.newBuilder().callTimeout(ms, TimeUnit.MILLISECONDS).build()

    /** wss://hub/v1/link → https://hub/v1/link/poll. */
    fun pollUrl(ws: String): String =
      ws.replaceFirst(Regex("^wss://"), "https://").replaceFirst(Regex("^ws://"), "http://").trimEnd('/') + "/poll"

    /** `POST <origin>/v1/actions/<token>`, no bearer, body {} (push.md 8); the status or null. */
    fun postAction(origin: String, token: String, timeoutMs: Long): Int? = try {
      timed(timeoutMs).newCall(Request.Builder().url("${origin.trimEnd('/')}/v1/actions/$token").post("{}".toRequestBody(JSON)).build())
        .execute().use { it.code }
    } catch (_: IOException) {
      null
    }

    /** The action transport without a link (no service, or no client yet): `POST <origin>/v1/actions/<token>` only. */
    val HTTP_ONLY = object : ActionTransport {
      override fun viaLink(id: String, token: String, timeoutMs: Long): Int? = null
      override fun viaHttp(origin: String, token: String, timeoutMs: Long): Int? = postAction(origin, token, timeoutMs)
    }

    /** A link URL (link.md 2): the WebSocket schemes only. */
    fun isLinkUrl(u: String) = u.startsWith("wss://") || u.startsWith("ws://")

    fun newActionId(): String = "act_" + UUID.randomUUID().toString().replace("-", "").take(16)
  }

  /** The long-poll loop (link.md 2.1). Each request is a fresh login with the previous reply's challenge. */
  class Poller(
    private val store: Store,
    private val signer: Signer,
    private val processor: FrameProcessor,
    private val client: () -> JSONObject,
    private val wake: (() -> Unit) -> Unit,
  ) {
    class Stop(val status: String, val why: String) : Exception(why)

    internal val http: OkHttpClient = timed(40_000).newBuilder().readTimeout(35, TimeUnit.SECONDS).build()

    private fun call(url: String, body: JSONObject): JSONObject {
      http.newCall(Request.Builder().url(url).post(body.toString().toRequestBody(JSON)).build()).execute().use { r ->
        when (r.code) {
          200 -> return JSONObject(r.body?.string() ?: "{}")
          403 -> throw Stop("revoked", "This phone was removed from the hub")
          426 -> throw Stop("upgrade", "Update the app")
          else -> throw IOException("http_${r.code}")
        }
      }
    }

    fun loop(url: String, until: Long = Long.MAX_VALUE, rounds: Int = Int.MAX_VALUE, alive: () -> Boolean, onUp: () -> Unit) {
      val pin = store.hubFp ?: throw Stop("error", "not configured")
      val verifier = TinkHubVerifier(store.hubPub ?: throw Stop("error", "not configured"))
      var challenge = call(url, JSONObject().put("op", "challenge"))
      var n = 0
      val rejects = mutableListOf<Long>() // frames that failed their checks: sent in the next `up` (link.md 2.1)
      while (alive() && n < rounds && System.currentTimeMillis() < until) {
        n++
        val c = Login.checkChallenge(challenge, pin, verifier)
          ?: if (Login.fromAnotherHub(challenge, pin, verifier)) throw Stop("changed", "Hub key changed: pair this phone again") else throw IOException("bad_challenge")
        val device = store.device ?: throw Stop("error", "not configured")
        val pending = store.pending(System.currentTimeMillis()).take(64)
        val up = JSONArray()
        rejects.take(64).forEach { up.put(JSONObject().put("t", "reject").put("seq", it)) }
        pending.take(64 - up.length()).forEach { up.put(JSONObject().put("t", "action").put("id", it.id).put("token", it.token)) }
        val req = JSONObject().put("op", "poll").put("hello", Login.hello(c, device, store.last, client(), signer))
        if (up.length() > 0) req.put("up", up)
        val reply = call(url, req)
        reply.optJSONObject("welcome")?.let { w -> wake { processor.onText(w.toString(), c.nonce) } }
        onUp()
        rejects.clear()
        val frames = reply.optJSONArray("frames") ?: JSONArray()
        for (i in 0 until frames.length()) {
          val f = frames.optJSONObject(i) ?: continue
          var out: Outcome? = null
          wake { out = processor.onText(f.toString(), Frames.QUEUE_CTX) } // acks travel as the next hello's `last`
          out?.reject?.let { rejects.add(it) }
        }
        val results = reply.optJSONArray("results") ?: JSONArray()
        for (i in 0 until results.length()) {
          val f = results.optJSONObject(i) ?: continue
          var out: Outcome? = null
          wake { out = processor.onText(f.toString(), c.nonce) }
          val (t, body) = out?.ephemeral ?: continue
          if (t == "action_result" && ActionRunner.final(body.optInt("status", 0))) {
            store.removePending(body.optString("id"), System.currentTimeMillis())
          }
        }
        challenge = reply.optJSONObject("next") ?: call(url, JSONObject().put("op", "challenge"))
      }
    }
  }
}
