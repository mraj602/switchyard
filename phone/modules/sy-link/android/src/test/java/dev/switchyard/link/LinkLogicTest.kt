package dev.switchyard.link

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class BackoffTest {
  @Test fun scheduleWithJitterBounds() {
    val steps = listOf(1, 2, 5, 10, 20, 30, 60, 120, 120, 120)
    val low = Backoff { 0.0 }
    val high = Backoff { 0.999999 }
    val mid = Backoff { 0.5 }
    for (s in steps) {
      assertEquals(s * 800L, low.next())
      assertTrue(high.next() in (s * 1199L)..(s * 1200L))
      assertEquals(s * 1000L, mid.next())
    }
    val random = Backoff()
    for (i in 0 until 50) assertTrue(random.next() in Backoff.bounds(i))
  }

  @Test fun resetsAfterAStableConnectionOrANetworkChange() {
    val b = Backoff { 0.5 }
    repeat(5) { b.next() }
    b.connectionLasted(59_999)
    assertEquals(30_000L, b.next())
    b.connectionLasted(60_000)
    assertEquals(1_000L, b.next())
    repeat(3) { b.next() }
    b.reset() // a network change
    assertEquals(1_000L, b.next())
  }
}

class NotifyIdsTest {
  // reference: python3 struct.unpack('>i', sha256(tag)[:4])
  @Test fun idIsTheFirstFourBytesOfSha256BigEndianSigned() {
    assertEquals(320096816, NotifyIds.forTag("sy-test"))
    assertEquals(1635722886, NotifyIds.forTag("sy-s-c41e0000-0000-4000-8000-000000000003"))
    assertEquals(-474954686, NotifyIds.forTag(""))
  }

  @Test fun theSameTagReplacesAndAnotherDoesNot() {
    assertEquals(NotifyIds.forTag("sy-s-a"), NotifyIds.forTag("sy-s-a"))
    assertTrue(NotifyIds.forTag("sy-s-a") != NotifyIds.forTag("sy-s-b"))
  }
}

class DedupeTest {
  @Test fun aReplayAfterARestartIsDroppedBySeq() {
    val p = TestPhone()
    val json = JSONObject().put("v", 1).put("type", "alert").put("title", "t").put("body", "b").put("tag", "sy-s-1")
      .put("url", "/#/").put("tap", "auto").put("needs_you", 1).put("private", false).put("ts", JSONObject.NULL).toString()
    val f1 = p.hub.frame(p.device, "alert", 1, p.nowS + 60, "sy-s-1", p.seal(json))
    val f2 = p.hub.frame(p.device, "alert", 2, p.nowS + 60, "sy-s-1", p.seal(json))
    assertEquals(1L, p.processor().onText(f1, "n").ack)

    // the process dies before the ack went out; a new process reads the same storage
    val sink = RecordingSink()
    val restarted = FrameProcessor(Store(p.kv), p.hub.verifier(), { Rfc8291.decrypt(it, p.agreement, p.auth) }, sink, { p.now })
    val replay = restarted.onText(f1, "n2")
    assertEquals("duplicate", replay.note)
    assertEquals(1L, replay.ack)
    assertTrue(sink.posted.isEmpty())
    assertEquals(2L, restarted.onText(f2, "n2").ack)
    assertEquals(1, sink.posted.size)
    assertEquals(2L, Store(p.kv).last)
  }

  @Test fun lastIsWrittenSynchronouslyBeforeTheAck() {
    val commits = ArrayList<Map<String, Any?>>()
    val kv = object : KV by MemoryKV() {
      val inner = MemoryKV()
      override fun getString(k: String) = inner.getString(k)
      override fun getLong(k: String, def: Long) = inner.getLong(k, def)
      override fun getBoolean(k: String, def: Boolean) = inner.getBoolean(k, def)
      override fun put(values: Map<String, Any?>, commit: Boolean) {
        if (commit && values.containsKey("last")) commits += values
        inner.put(values, commit)
      }
      override fun clear() = inner.clear()
    }
    val p = TestPhone(kv = MemoryKV())
    val store = Store(kv)
    store.saveKeys(p.auth)
    store.configure("https://h", p.device, listOf("wss://h/v1/link"), 180, p.hub.pub, p.hub.fp, "qr")
    commits.clear()
    val json = JSONObject().put("v", 1).put("type", "test").put("title", "t").put("body", "b").put("tag", "sy-test").toString()
    val proc = FrameProcessor(store, p.hub.verifier(), { Rfc8291.decrypt(it, p.agreement, p.auth) }, RecordingSink(), { p.now })
    proc.onText(p.hub.frame(p.device, "alert", 7, p.nowS + 60, "sy-test", p.seal(json)), "n")
    assertEquals(listOf(mapOf<String, Any?>("last" to 7L)), commits)
  }
}

class ActionsTest {
  private class FakeTransport(var link: Int?, var http: Int?) : ActionTransport {
    val log = ArrayList<String>()
    var clock = 0L
    var linkCost = 0L

    override fun viaLink(id: String, token: String, timeoutMs: Long): Int? {
      log += "link:$timeoutMs"
      clock += linkCost
      return link
    }
    override fun viaHttp(origin: String, token: String, timeoutMs: Long): Int? {
      log += "http:$timeoutMs"
      return http
    }
  }

  private fun action(at: Long = 0) = PendingAction("act_1", "a1.x.y", "seen", "al_1", "https://hub", at)

  @Test fun theLinkFirst() {
    val t = FakeTransport(200, 200)
    val s = Store(MemoryKV())
    assertEquals(ActionOutcome.LINK, ActionRunner(s, t, { t.clock }).run(action()))
    assertEquals(listOf("link:5000"), t.log)
  }

  @Test fun thenTheActionUrlWithWhatIsLeftOfTheBudget() {
    val t = FakeTransport(null, 200).apply { linkCost = 5_000 }
    val s = Store(MemoryKV())
    assertEquals(ActionOutcome.HTTP, ActionRunner(s, t, { t.clock }).run(action()))
    assertEquals(listOf("link:5000", "http:10000"), t.log)
    t.log.clear()
    assertEquals(ActionOutcome.HTTP, ActionRunner(s, t, { t.clock }).run(action(), budgetMs = 9_000))
    assertEquals(listOf("link:5000", "http:4000"), t.log)
  }

  @Test fun thenTheQueueFlushedAtTheNextConnection() {
    val t = FakeTransport(null, null)
    val s = Store(MemoryKV())
    val r = ActionRunner(s, t, { t.clock })
    assertEquals(ActionOutcome.QUEUED, r.run(action()))
    assertEquals(1, s.pending(0).size)
    t.link = 429 // not final: stays
    assertEquals(0, r.flush())
    assertEquals(1, s.pending(0).size)
    t.link = 200
    assertEquals(1, r.flush())
    assertTrue(s.pending(0).isEmpty())
  }

  @Test fun finalAnswersAreNotRetried() {
    for (code in listOf(401, 404, 410)) {
      val t = FakeTransport(code, 200)
      val s = Store(MemoryKV())
      assertEquals(ActionOutcome.FINAL_REFUSED, ActionRunner(s, t, { 0 }).run(action()))
      assertEquals(listOf("link:5000"), t.log)
      assertTrue(s.pending(0).isEmpty())
    }
    val t = FakeTransport(null, 410)
    assertEquals(ActionOutcome.FINAL_REFUSED, ActionRunner(Store(MemoryKV()), t, { 0 }).run(action()))
    val busy = FakeTransport(429, 503)
    val s = Store(MemoryKV())
    assertEquals(ActionOutcome.QUEUED, ActionRunner(s, busy, { 0 }).run(action()))
  }

  @Test fun theQueueKeepsFiftyAndDropsAfter24Hours() {
    val s = Store(MemoryKV())
    for (i in 0 until 60) s.addPending(PendingAction("act_$i", "a1.x.y", "seen", "al", "https://h", i * 1000L), 60_000)
    assertEquals(50, s.pending(60_000).size)
    assertEquals("act_10", s.pending(60_000).first().id)
    assertTrue(s.pending(Store.PENDING_TTL_MS + 60_000).isEmpty())
  }
}

class CloseCodesTest {
  @Test fun eachCodeDoesWhatLinkMd8Says() {
    assertEquals("revoked", (CloseCodes.decide(4403, 1) as CloseAction.Stop).status)
    assertEquals("upgrade", (CloseCodes.decide(4426, 1) as CloseAction.Stop).status)
    assertEquals(CloseAction.Superseded, CloseCodes.decide(4409, 1))
    assertEquals(CloseAction.Retry, CloseCodes.decide(4401, 2))
    assertEquals("Hub does not recognise this device's key", (CloseCodes.decide(4401, 3) as CloseAction.Stop).why)
    assertEquals(CloseAction.Retry, CloseCodes.decide(4400, 1))
    assertTrue(CloseCodes.decide(4400, 3) is CloseAction.Stop)
    assertEquals(CloseAction.After(60_000), CloseCodes.decide(4429, 1))
    assertEquals(CloseAction.After(60_000), CloseCodes.decide(4503, 1))
    for (c in listOf(1001, 4408, 4413, 4500, 0)) assertEquals(CloseAction.Retry, CloseCodes.decide(c, 5))
    assertEquals("superseded", CloseCodes.word(4409))
  }
}

class ForegroundTest {
  @Test fun api34UsesSpecialUseAndApi31To33TheTwoArgumentForm() {
    for (sdk in listOf(31, 32, 33)) {
      val calls = ArrayList<String>()
      Foreground.go(sdk, "n", { calls += "plain:$it" }, { n, t -> calls += "typed:$n:$t" })
      assertEquals(listOf("plain:n"), calls)
    }
    for (sdk in listOf(34, 35, 36)) {
      val calls = ArrayList<String>()
      Foreground.go(sdk, "n", { calls += "plain:$it" }, { n, t -> calls += "typed:$n:$t" })
      assertEquals(listOf("typed:n:1073741824"), calls) // ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
    }
  }

  @Test fun aRefusedStartReturnsFalseInsteadOfCrashing() {
    // ForegroundServiceStartNotAllowedException is an IllegalStateException (a START_STICKY
    // restart from the background on API 31+); 34+ may also throw SecurityException for the type.
    assertFalse(Foreground.go(31, "n", { throw IllegalStateException("not allowed") }, { _, _ -> }))
    assertFalse(Foreground.go(34, "n", { }, { _, _ -> throw SecurityException("type") }))
    assertTrue(Foreground.go(34, "n", { }, { _, _ -> }))
  }
}

class PairingTest {
  private val hub = HubKey()
  private val sign = SoftwareSigner().publicKey
  private val box = SoftwareAgreement.fresh().publicKey
  private val device = "dev_01JA4"

  private fun cert(body: JSONObject = certBody(), signer: HubKey = hub, extra: JSONObject? = null): JSONObject {
    val bytes = Codec.utf8(body.toString())
    val sigs = org.json.JSONArray().put(
      JSONObject().put("by", "hub").put("fp", signer.fp).put("sig", Codec.b64u(signer.sign(Docs.signedInput("device_cert", bytes)))),
    )
    extra?.let { sigs.put(it) }
    return JSONObject().put("type", "device_cert").put("body", Codec.b64u(bytes)).put("sigs", sigs)
  }

  private fun certBody(reg: String = "qr") = JSONObject().put("v", 1).put("device", device).put("kind", "phone").put("name", "Pixel · app")
    .put("machine", JSONObject.NULL).put("sign", JSONObject().put("alg", "p256").put("pub", Codec.b64u(sign)))
    .put("box", JSONObject().put("alg", "p256").put("pub", Codec.b64u(box))).put("reg", reg).put("hub", hub.fp)
    .put("issued_at", "2026-10-15T09:00:00.000Z")

  private fun refused(code: String, f: () -> Unit) {
    try {
      f()
      fail("accepted")
    } catch (e: Pairing.Refused) {
      assertEquals(code, e.code)
    }
  }

  @Test fun aGoodQrReplyPasses() {
    Pairing.check(hub.pub, hub.fp, hub.fp, device, "qr", cert(), sign, box)
    Pairing.check(hub.pub, hub.fp, null, device, "tofu", cert(certBody("tofu")), sign, box)
  }

  @Test fun everyMismatchIsRefused() {
    val other = HubKey(ByteArray(32) { 3 })
    refused("hub_key_mismatch") { Pairing.check(hub.pub, other.fp, null, device, "qr", cert(), sign, box) }
    refused("hub_key_mismatch") { Pairing.check(hub.pub, hub.fp, other.fp, device, "qr", cert(), sign, box) }
    refused("tampered") { Pairing.check(hub.pub, hub.fp, hub.fp, device, "tofu", cert(certBody("tofu")), sign, box) }
    refused("tampered") { Pairing.check(hub.pub, hub.fp, hub.fp, device, "qr", null, sign, box) }
    refused("tampered") { Pairing.check(hub.pub, hub.fp, hub.fp, device, "qr", cert(signer = other), sign, box) }
    refused("tampered") { Pairing.check(hub.pub, hub.fp, hub.fp, "dev_other", "qr", cert(), sign, box) }
    refused("tampered") { Pairing.check(hub.pub, hub.fp, hub.fp, device, "qr", cert(), SoftwareSigner().publicKey, box) }
    refused("tampered") { Pairing.check(hub.pub, hub.fp, hub.fp, device, "qr", cert(), sign, SoftwareAgreement.fresh().publicKey) }
    refused("tampered") {
      Pairing.check(hub.pub, hub.fp, hub.fp, device, "qr", cert(extra = JSONObject().put("by", "hub").put("sig", "AA")), sign, box)
    }
  }

  @Test fun theSealedKeysOpenOnTheHubSide() {
    val k = ByteArray(16) { 8 }
    val json = Pairing.keysJson(sign, box, ByteArray(16) { 1 })
    val sealed = ClaimSeal.seal(k, "S".repeat(22), Codec.utf8(json.toString()), ByteArray(12))
    val opened = JSONObject(String(ClaimSeal.open(k, "S".repeat(22), sealed), Charsets.UTF_8))
    assertEquals("p256", opened.getJSONObject("box").getString("alg"))
    assertEquals(Codec.b64u(box), opened.getJSONObject("box").getString("pub"))
    assertFalse(opened.has("priv"))
  }
}

class SharedHttpTest {
  @Test fun reconnectsPollsAndActionsShareTheOnePoolAndDispatcher() {
    val p = TestPhone()
    val events = object : LinkEvents {
      override fun state() = Unit
      override fun stopped(status: String, why: String) = Unit
      override fun longTimer(atMs: Long) = Unit
    }
    val c = LinkClient(p.store, SoftwareSigner(), p.processor(), events, { JSONObject() }, { it() })
    try {
      val clients = listOf(
        c.wsHttp(180), c.wsHttp(600), // two reconnects, the second after a ping change
        LinkClient.Poller(p.store, SoftwareSigner(), p.processor(), { JSONObject() }, { it() }).http,
        LinkClient.timed(1500),
      )
      for (x in clients) {
        assertSame(LinkClient.HTTP.dispatcher, x.dispatcher)
        assertSame(LinkClient.HTTP.connectionPool, x.connectionPool)
        assertFalse(x.followRedirects)
      }
      assertEquals(600_000, clients[1].pingIntervalMillis)
      assertEquals(0, clients[0].readTimeoutMillis)
    } finally {
      c.shutdown()
    }
  }
}
