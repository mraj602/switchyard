package dev.watchyard.link

import org.json.JSONArray
import org.json.JSONObject

/**
 * The few storage calls the module makes, so the JVM tests use a map and production the
 * module's own SharedPreferences (push.md 14.4 "Storage"). `commit` is synchronous: `last` is
 * stored with it before an ack goes out (link.md 5.6).
 */
interface KV {
  fun getString(k: String): String?
  fun getLong(k: String, def: Long): Long
  fun getBoolean(k: String, def: Boolean): Boolean
  fun put(values: Map<String, Any?>, commit: Boolean = false)
  fun clear()
}

/** One action waiting for a connection (push.md 14.6 step 3). Never logged. */
data class PendingAction(val id: String, val token: String, val action: String, val alert: String, val origin: String, val at: Long) {
  fun toJson(): JSONObject = JSONObject()
    .put("id", id).put("token", token).put("action", action).put("alert", alert).put("origin", origin).put("at", at)

  companion object {
    fun fromJson(o: JSONObject): PendingAction? = try {
      PendingAction(o.getString("id"), o.getString("token"), o.getString("action"), o.optString("alert"), o.optString("origin"), o.getLong("at"))
    } catch (_: Exception) {
      null
    }
  }
}

/** Typed access to the module's state. Everything the service needs survives a process death. */
class Store(private val kv: KV) {
  // pairing (configure)
  val configured get() = kv.getBoolean("configured", false)
  val origin get() = kv.getString("origin")
  val device get() = kv.getString("device")
  val hubPub get() = Codec.unb64u(kv.getString("hub_pub"))
  val hubFp get() = kv.getString("hub_fp")
  val reg get() = kv.getString("reg") ?: "tofu"
  val verified get() = kv.getString("verified") ?: reg
  val urls: List<String> get() = kv.getString("urls")?.let { s -> JSONArray(s).let { a -> List(a.length()) { a.getString(it) } } } ?: emptyList()
  val pingS get() = kv.getLong("ping_s", DEFAULT_PING_S)
  val snoozeMin get() = kv.getLong("snooze_min", 15)

  /** The RFC 8291 auth secret (14.3: kept as it is; on its own it opens nothing). */
  val auth get() = Codec.unb64u(kv.getString("auth"))

  /** prepareKeys() keys not used by a configure yet: reused by the next prepareKeys (14.8). */
  val keysFresh get() = kv.getBoolean("keys_fresh", false)
  fun setKeysFresh(on: Boolean) = kv.put(mapOf("keys_fresh" to on), commit = true)

  // the switch and the queue position
  val enabled get() = kv.getBoolean("enabled", false)
  val last get() = kv.getLong("last", 0)
  val offsetMs get() = kv.getLong("offset_ms", 0)

  // shown in Settings → Notifications
  val received get() = kv.getLong("received", 0)
  val rejected get() = kv.getLong("rejected", 0)
  val status get() = kv.getString("status") ?: "off"
  val transport get() = kv.getString("transport")
  val connectedSince get() = kv.getLong("connected_since", 0).takeIf { it > 0 }
  val nextRetryAt get() = kv.getLong("next_retry_at", 0).takeIf { it > 0 }
  val lastError get() = kv.getString("last_error")
  val refresh get() = kv.getString("refresh")

  fun configure(origin: String, device: String, urls: List<String>, pingS: Long, hubPub: ByteArray, hubFp: String, reg: String) {
    kv.put(
      mapOf(
        "configured" to true, "origin" to origin, "device" to device, "urls" to JSONArray(urls).toString(),
        "ping_s" to pingS, "hub_pub" to Codec.b64u(hubPub), "hub_fp" to hubFp, "reg" to reg, "verified" to reg,
        "last" to 0L, "status" to "off", "last_error" to null,
      ),
      commit = true,
    )
  }

  fun saveKeys(auth: ByteArray) = kv.put(mapOf("auth" to Codec.b64u(auth), "keys_fresh" to true), commit = true)

  fun setEnabled(on: Boolean) = kv.put(mapOf("enabled" to on), commit = true)
  fun setPing(s: Long) = kv.put(mapOf("ping_s" to s))
  fun setSnooze(min: Long) = kv.put(mapOf("snooze_min" to min))
  fun setUrls(urls: List<String>) = kv.put(mapOf("urls" to JSONArray(urls).toString()))
  fun setOffset(ms: Long) = kv.put(mapOf("offset_ms" to ms))
  fun setVerified(v: String) = kv.put(mapOf("verified" to v), commit = true)
  fun setRefresh(json: String) = kv.put(mapOf("refresh" to json))

  /** Durable before the ack (link.md 5.6): a synchronous commit. */
  fun setLast(seq: Long) = kv.put(mapOf("last" to seq), commit = true)

  fun countReceived() = kv.put(mapOf("received" to received + 1))
  fun countRejected() = kv.put(mapOf("rejected" to rejected + 1))

  fun setConnection(status: String, transport: String?, connectedSince: Long?, nextRetryAt: Long?, lastError: String?) =
    kv.put(
      mapOf(
        "status" to status, "transport" to transport, "connected_since" to (connectedSince ?: 0L),
        "next_retry_at" to (nextRetryAt ?: 0L), "last_error" to lastError,
      ),
    )

  fun setError(err: String?) = kv.put(mapOf("last_error" to err))

  // pending actions (14.6 step 3): at most 50, dropped after 24 h
  fun pending(now: Long): List<PendingAction> {
    val s = kv.getString("pending") ?: return emptyList()
    val a = try { JSONArray(s) } catch (_: Exception) { return emptyList() }
    return List(a.length()) { a.optJSONObject(it)?.let(PendingAction::fromJson) }
      .filterNotNull().filter { now - it.at < PENDING_TTL_MS }
  }

  fun savePending(list: List<PendingAction>) {
    val a = JSONArray()
    list.takeLast(PENDING_MAX).forEach { a.put(it.toJson()) }
    kv.put(mapOf("pending" to a.toString()), commit = true)
  }

  fun addPending(p: PendingAction, now: Long) = savePending(pending(now).filter { it.id != p.id } + p)

  fun removePending(id: String, now: Long) = savePending(pending(now).filter { it.id != id })

  /** Unpair (14.3 reset): everything goes. */
  fun clear() = kv.clear()

  companion object {
    const val DEFAULT_PING_S = 180L
    const val PENDING_MAX = 50
    const val PENDING_TTL_MS = 24 * 3600 * 1000L
  }
}
