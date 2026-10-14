package dev.switchyard.link

/**
 * push.md 14.2 "Starting in the foreground, per API level": API 34+ starts with type
 * `specialUse`; 31–33 do not know that type and use the two-argument form. Generic over the
 * notification so the JVM test runs both branches without Android classes.
 */
object Foreground {
  /** ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE (API 34). */
  const val TYPE_SPECIAL_USE = 1 shl 30
  const val API_SPECIAL_USE = 34

  /**
   * False when Android refused: ForegroundServiceStartNotAllowedException (an
   * IllegalStateException) on a background start such as a START_STICKY restart on API 31+,
   * or a SecurityException for the type on 34+. The caller stops instead of crashing.
   */
  fun <N> go(sdk: Int, n: N, plain: (N) -> Unit, typed: (N, Int) -> Unit): Boolean = try {
    if (sdk >= API_SPECIAL_USE) typed(n, TYPE_SPECIAL_USE) else plain(n)
    true
  } catch (_: IllegalStateException) {
    false
  } catch (_: SecurityException) {
    false
  }
}
