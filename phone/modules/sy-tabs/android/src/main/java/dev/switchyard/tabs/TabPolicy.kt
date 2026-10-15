package dev.switchyard.tabs

import java.net.URI

/**
 * The pure rules of phone.md P17.1 and P17.5: profile names from seat ids, and where a
 * main-frame URL may go. No Android types, so the JVM tests cover all of it.
 */
object TabPolicy {
  /** A hub seat id (identity.md 1): `seat_` + a ULID. */
  private val SEAT = Regex("^seat_[0-9A-HJKMNP-TV-Z]{26}$")
  const val PREFIX = "sy-tab-"
  const val HOME = "https://claude.ai/"

  /** claude.ai and the sign-in hops (desktop.md C2), each with its subdomains. */
  val HOSTS = listOf("claude.ai", "anthropic.com", "claude.com", "challenges.cloudflare.com")

  enum class Route { TAB, EXTERNAL, DROP }

  fun validSeat(seat: String?): Boolean = seat != null && SEAT.matches(seat)

  /** The profile of a seat, or null for anything that is not a seat id (never an email). */
  fun profileName(seat: String?): String? = if (validSeat(seat)) PREFIX + seat else null

  /** The seat of one of our profiles, or null for any other profile (`Default` included). */
  fun seatOf(profile: String?): String? {
    if (profile == null || !profile.startsWith(PREFIX)) return null
    val seat = profile.removePrefix(PREFIX)
    return if (validSeat(seat)) seat else null
  }

  /** The seat ids to list: tab profiles only, minus the ones signed out and waiting for deletion. */
  fun listed(profiles: List<String>, pending: Set<String>): List<String> =
    profiles.filter { it !in pending }.mapNotNull(::seatOf).sorted()

  fun allowedHost(host: String?): Boolean {
    if (host.isNullOrEmpty()) return false
    val h = host.lowercase().removeSuffix(".")
    return HOSTS.any { h == it || h.endsWith(".$it") }
  }

  private fun parse(url: String?): URI? =
    if (url == null || url.length > 8192 || url.any { it.isWhitespace() || it == '\\' }) null
    else try { URI(url) } catch (_: Exception) { null }

  /** Where a main-frame navigation goes: the tab, the browser, or nowhere. */
  fun route(url: String?): Route {
    val u = parse(url) ?: return Route.DROP
    return when (u.scheme?.lowercase()) {
      "https" -> when {
        u.rawUserInfo != null || u.host.isNullOrEmpty() -> Route.DROP
        allowedHost(u.host) && (u.port == -1 || u.port == 443) -> Route.TAB
        else -> Route.EXTERNAL
      }
      "mailto" -> Route.EXTERNAL
      else -> Route.DROP
    }
  }

  /** The first URL a tab loads must be one the tab may show; anything else starts at claude.ai. */
  fun startUrl(url: String?): String = if (route(url) == Route.TAB) url!! else HOME
}
