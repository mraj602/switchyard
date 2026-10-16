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

  // ---- P17.7: moved profiles

  private val b = "seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4M"
  private val c = "seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4N"

  @Test fun aSecondProfileOfASeatCarriesItsSeat() {
    assertEquals(seat, TabPolicy.seatOf("sy-tab-$seat.1"))
    assertEquals(seat, TabPolicy.seatOf("sy-tab-$seat.12"))
    for (bad in listOf("sy-tab-$seat.0", "sy-tab-$seat.", "sy-tab-$seat.1000", "sy-tab-$seat.x", "sy-tab-$seat.1.2")) {
      assertNull(bad, TabPolicy.seatOf(bad))
    }
  }

  @Test fun aMovedProfileBelongsToItsNewSeat() {
    val moves = mapOf("sy-tab-$seat" to b)
    assertEquals(b, TabPolicy.ownerOf("sy-tab-$seat", moves))
    assertEquals(seat, TabPolicy.ownerOf("sy-tab-$seat", emptyMap()))
    assertNull(TabPolicy.ownerOf("Default", mapOf("Default" to b)))
    assertEquals(listOf(b), TabPolicy.listed(listOf("sy-tab-$seat", "Default"), emptySet(), moves))
  }

  @Test fun moveGivesTheProfileToASeatWithoutOne() {
    val existing = listOf("sy-tab-$seat")
    val (moves, err) = TabPolicy.move(seat, b, existing, emptyMap())
    assertNull(err)
    assertEquals(mapOf("sy-tab-$seat" to b), moves)
    // The seat it came from now gets a fresh name, never the one b owns.
    assertEquals("sy-tab-$seat.1", TabPolicy.profileFor(seat, existing, moves!!))
    assertEquals("sy-tab-$seat", TabPolicy.profileFor(b, existing, moves))
    // Moving it back drops the entry.
    val (back, err2) = TabPolicy.move(b, seat, existing, moves)
    assertNull(err2)
    assertEquals(emptyMap<String, String>(), back)
  }

  @Test fun moveIsRefusedWhenTheOtherSeatHasATabOrThereIsNone() {
    val existing = listOf("sy-tab-$seat", "sy-tab-$b")
    assertEquals(TabPolicy.MoveError.TAKEN, TabPolicy.move(seat, b, existing, emptyMap()).second)
    assertEquals(TabPolicy.MoveError.NONE, TabPolicy.move(c, b, existing, emptyMap()).second)
    assertEquals(TabPolicy.MoveError.SEAT, TabPolicy.move(seat, "me@example.com", existing, emptyMap()).second)
    assertEquals(TabPolicy.MoveError.SEAT, TabPolicy.move(seat, seat, existing, emptyMap()).second)
  }

  @Test fun profileForPrefersTheProfileTheSeatHas() {
    val moves = mapOf("sy-tab-$seat" to b)
    // seat's own name went to b; seat signed in again under .1; then b's tab was signed out.
    assertEquals("sy-tab-$seat.1", TabPolicy.profileFor(seat, listOf("sy-tab-$seat.1"), emptyMap()))
    assertEquals("sy-tab-$seat.1", TabPolicy.profileFor(seat, listOf("sy-tab-$seat", "sy-tab-$seat.1"), moves))
    assertEquals("sy-tab-$c", TabPolicy.profileFor(c, emptyList(), moves))
    assertNull(TabPolicy.profileFor("me@example.com", emptyList(), moves))
  }

  @Test fun movesRoundTripAndDropBadEntries() {
    val m = mapOf("sy-tab-$seat" to b, "sy-tab-$c.2" to seat)
    assertEquals(m, TabPolicy.decodeMoves(TabPolicy.encodeMoves(m)))
    assertEquals(emptyMap<String, String>(), TabPolicy.decodeMoves(setOf("Default=$b", "sy-tab-$seat=me@example.com", "=x", "junk")))
    assertEquals(emptyMap<String, String>(), TabPolicy.decodeMoves(null))
  }

  // ---- P17.7: the account check

  @Test fun readableIsASignedInClaudePage() {
    for (u in listOf("https://claude.ai/", "https://claude.ai/new", "https://claude.ai/chat/x", "https://www.claude.ai/x", "https://claude.ai/loginx")) {
      assertTrue(u, TabPolicy.readable(u))
    }
    for (u in listOf(
      "https://claude.ai/login", "https://claude.ai/login/", "https://claude.ai/login?x=1", "https://claude.ai/logout",
      "https://claude.ai/login/magic", "https://console.anthropic.com/", "http://claude.ai/", "https://claude.ai:8443/",
      "https://claude.ai.evil.com/", null, "", "about:blank",
    )) {
      assertFalse(u.toString(), TabPolicy.readable(u))
    }
  }

  @Test fun theCheckRunsOnClaudeAiItselfOnly() {
    assertTrue(TabPolicy.checkable("https://claude.ai/new"))
    assertTrue(TabPolicy.checkable("https://CLAUDE.AI./new"))
    assertFalse(TabPolicy.checkable("https://www.claude.ai/new"))
    assertFalse(TabPolicy.checkable("https://claude.ai/login"))
  }

  @Test fun theScriptAsksForTheAccountOnly() {
    val n = "0123456789abcdef"
    val s = TabPolicy.accountStart(n)
    assertTrue(s.contains("fetch('/api/account'"))
    assertEquals(1, Regex("fetch\\(").findAll(s).count())
    for (bad in listOf("cookie", "document", "localStorage", "sessionStorage", "indexedDB", "innerHTML", "postMessage", "XMLHttpRequest")) {
      assertFalse(bad, s.contains(bad, ignoreCase = true))
      assertFalse(bad, TabPolicy.accountPoll(n).contains(bad, ignoreCase = true))
    }
    assertTrue(s.contains("__sy_acct_$n"))
  }

  @Test(expected = IllegalArgumentException::class)
  fun theNonceIsHexOnly() {
    TabPolicy.accountStart("x';alert(1);'")
  }

  @Test fun parseAccountTakesAUuidOrAStatus() {
    val u = "3f1c0000-0000-4000-8000-000000000001"
    assertEquals(TabPolicy.Account(u, null), TabPolicy.parseAccount("\"u:$u\""))
    assertEquals(TabPolicy.Account(null, 401), TabPolicy.parseAccount("\"s:401\""))
    assertEquals(TabPolicy.Account(null, 0), TabPolicy.parseAccount("\"s:0\""))
    for (bad in listOf(null, "null", "\"\"", "\"u:$u", "u:$u", "\"u:me@example.com\"", "\"s:4011\"", "\"u:${u.uppercase()}\"", "{\"uuid\":\"$u\"}")) {
      assertNull(bad, TabPolicy.parseAccount(bad))
    }
  }
}
