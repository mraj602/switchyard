package dev.switchyard.link

/** phone.md P6.1's channels; the app's JS creates the same ids, so either side may go first. */
object Channels {
  const val NEEDS_YOU = "sy_needs_you"
  const val LIMITS = "sy_limits"
  const val MACHINES = "sy_machines"
  const val TEST = "sy_test"
  const val CONNECTION = "sy_connection"

  /** id, name, importance (NotificationManager's IMPORTANCE_* values). */
  val ALL = listOf(
    Triple(NEEDS_YOU, "Needs you", 4),
    Triple(LIMITS, "Limits", 3),
    Triple(MACHINES, "Machines", 2),
    Triple(TEST, "Test", 3),
    Triple(CONNECTION, "Connection (always on)", 1),
  )

  const val GROUP = "sy"
}
