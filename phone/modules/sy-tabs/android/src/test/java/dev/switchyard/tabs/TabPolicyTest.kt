package dev.switchyard.tabs

import dev.switchyard.tabs.TabPolicy.Route
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class TabPolicyTest {
  private val seat = "seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"

  @Test fun profileNameIsTheSeatId() {
    assertEquals("sy-tab-$seat", TabPolicy.profileName(seat))
    assertEquals(seat, TabPolicy.seatOf("sy-tab-$seat"))
  }

  @Test fun profileNameRefusesAnythingButASeatId() {
    val bad = listOf(
      null, "", "me@example.com", "seat_", "seat_01ja2w5n0g8h3q7s2v6x9y1z4k", "seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4KX",
      "seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4I", "../seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", "Default",
    )
    for (b in bad) assertNull(b, TabPolicy.profileName(b))
  }

  @Test fun listedHidesPendingAndForeignProfiles() {
    val other = "seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4M"
    val all = listOf("Default", "sy-tab-$other", "sy-tab-$seat", "other-$seat")
    assertEquals(listOf(seat, other).sorted(), TabPolicy.listed(all, emptySet()))
    assertEquals(listOf(other), TabPolicy.listed(all, setOf("sy-tab-$seat")))
    assertEquals(emptyList<String>(), TabPolicy.listed(all, setOf("sy-tab-$seat", "sy-tab-$other")))
  }

  @Test fun seatOfIgnoresOtherProfiles() {
    assertNull(TabPolicy.seatOf("Default"))
    assertNull(TabPolicy.seatOf("sy-tab-me@example.com"))
    assertNull(TabPolicy.seatOf("other-$seat"))
    assertNull(TabPolicy.seatOf(null))
  }

  @Test fun claudeAndSignInHostsStayInTheTab() {
    val ok = listOf(
      "https://claude.ai/", "https://claude.ai/code/session_01abcDEF", "https://CLAUDE.AI/new", "https://www.claude.ai/x",
      "https://console.anthropic.com/login", "https://anthropic.com", "https://claude.com/login",
      "https://challenges.cloudflare.com/cdn-cgi/x", "https://claude.ai:443/", "https://claude.ai./",
    )
    for (u in ok) assertEquals(u, Route.TAB, TabPolicy.route(u))
  }

  @Test fun lookalikesAndOtherLinksGoToTheBrowser() {
    val ext = listOf(
      "https://claude.ai.evil.com/", "https://evilclaude.ai/", "https://notanthropic.com/", "https://cloudflare.com/",
      "https://github.com/anthropics", "https://claude.ai:8443/", "mailto:support@anthropic.com",
    )
    for (u in ext) assertEquals(u, Route.EXTERNAL, TabPolicy.route(u))
  }

  @Test fun everythingElseIsDropped() {
    val drop = listOf(
      null, "", "http://claude.ai/", "javascript:alert(1)", "intent://claude.ai#Intent;end", "file:///sdcard/x",
      "data:text/html,hi", "content://x", "claude://open", "https://user@claude.ai/", "https://claude.ai\\@evil.com/",
      "https://claude.ai /", "about:blank", "https:///nohost",
    )
    for (u in drop) assertEquals(u.toString(), Route.DROP, TabPolicy.route(u))
  }

  @Test fun startUrlFallsBackToHome() {
    assertEquals("https://claude.ai/code/session_01abc", TabPolicy.startUrl("https://claude.ai/code/session_01abc"))
    assertEquals(TabPolicy.HOME, TabPolicy.startUrl("https://example.com/"))
    assertEquals(TabPolicy.HOME, TabPolicy.startUrl("http://claude.ai/"))
    assertEquals(TabPolicy.HOME, TabPolicy.startUrl(null))
  }

  @Test fun allowedHostMatchesWholeLabels() {
    assertTrue(TabPolicy.allowedHost("a.b.claude.ai"))
    assertFalse(TabPolicy.allowedHost("claude.ai.example"))
    assertFalse(TabPolicy.allowedHost("xclaude.ai"))
    assertFalse(TabPolicy.allowedHost(""))
  }
}
