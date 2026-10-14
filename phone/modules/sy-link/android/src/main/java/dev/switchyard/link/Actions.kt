package dev.switchyard.link

/**
 * push.md 14.6: Snooze and Seen with the app killed. In order: the link (5 s for
 * `action_result`), then `POST <origin>/v1/actions/<token>` (10 s), then the local queue,
 * flushed over the link at the next connection. A 2xx, 401, 404 or 410 answer is final.
 * Pure: both transports and the clock are passed in, so the order is tested with fakes.
 */
interface ActionTransport {
  /** The status of `action_result`, or null when the link is down or did not answer in time. */
  fun viaLink(id: String, token: String, timeoutMs: Long): Int?

  /** The HTTP status, or null when the hub could not be reached. */
  fun viaHttp(origin: String, token: String, timeoutMs: Long): Int?
}

enum class ActionOutcome { LINK, HTTP, QUEUED, FINAL_REFUSED }

class ActionRunner(
  private val store: Store,
  private val transport: ActionTransport,
  private val nowMs: () -> Long,
) {
  fun run(a: PendingAction, budgetMs: Long = LINK_MS + HTTP_MS): ActionOutcome {
    val start = nowMs()
    transport.viaLink(a.id, a.token, LINK_MS)?.let { s ->
      if (final(s)) return if (ok(s)) ActionOutcome.LINK else ActionOutcome.FINAL_REFUSED
    }
    val left = budgetMs - (nowMs() - start)
    if (left > 0 && a.origin.isNotEmpty()) {
      transport.viaHttp(a.origin, a.token, minOf(HTTP_MS, left))?.let { s ->
        if (final(s)) return if (ok(s)) ActionOutcome.HTTP else ActionOutcome.FINAL_REFUSED
      }
    }
    store.addPending(a, nowMs())
    return ActionOutcome.QUEUED
  }

  /** At the next connection: every queued action over the link; final answers leave the queue. */
  fun flush(): Int {
    var done = 0
    for (a in store.pending(nowMs())) {
      val s = transport.viaLink(a.id, a.token, LINK_MS) ?: break
      if (final(s)) {
        store.removePending(a.id, nowMs())
        done++
      }
    }
    // the TTL drop of 14.6 (tokens expire after 24 h) happens on every read; persist it
    store.savePending(store.pending(nowMs()))
    return done
  }

  companion object {
    const val LINK_MS = 5_000L
    const val HTTP_MS = 10_000L

    fun ok(s: Int) = s in 200..299
    fun final(s: Int) = ok(s) || s == 401 || s == 404 || s == 410
  }
}
