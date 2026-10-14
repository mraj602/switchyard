package dev.switchyard.link

/**
 * link.md 8 / push.md 14.4: 1, 2, 5, 10, 20, 30, 60 s, then 120 s, each ±20 % jitter; reset
 * after a connection that lasted 60 s, and at once on a network change. `random` returns a
 * value in [0, 1) (injected, so tests can pin the jitter).
 */
class Backoff(private val random: () -> Double = { Math.random() }) {
  private var attempt = 0

  val attempts get() = attempt

  /** The delay before the next try, in ms, and counts the try. */
  fun next(): Long {
    val base = STEPS_S[minOf(attempt, STEPS_S.size - 1)] * 1000L
    attempt++
    val r = random().coerceIn(0.0, 1.0)
    return (base * (1 - JITTER + 2 * JITTER * r)).toLong()
  }

  fun reset() {
    attempt = 0
  }

  /** A connection closed after lasting `ms`: it resets the schedule when it lasted long enough. */
  fun connectionLasted(ms: Long) {
    if (ms >= STABLE_MS) reset()
  }

  companion object {
    val STEPS_S = longArrayOf(1, 2, 5, 10, 20, 30, 60, 120)
    const val JITTER = 0.2
    const val STABLE_MS = 60_000L

    /** Bounds of the n-th delay (0-based), for tests and the status line. */
    fun bounds(n: Int): LongRange {
      val base = STEPS_S[minOf(n, STEPS_S.size - 1)] * 1000L
      return (base * (1 - JITTER)).toLong()..(base * (1 + JITTER)).toLong()
    }
  }
}
