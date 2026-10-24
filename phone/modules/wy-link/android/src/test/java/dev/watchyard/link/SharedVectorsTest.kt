package dev.watchyard.link

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.File

/**
 * The Go side's goldens (link.md 10, the .json files in `internal/link/testdata`), read in place: the
 * module's build.gradle passes the directory as `wy.linkVectors`. Skipped while the directory
 * does not exist. Each file is `{"name", "keys", "fields", "signed_hex", "frame"}`; this runner
 * reads the names push.md 14.10 lists and skips a case whose keys it does not find.
 */
class SharedVectorsTest {
  private val dir = System.getProperty("wy.linkVectors")?.let(::File)

  private fun cases(): List<JSONObject> {
    assumeTrue("no shared vectors yet (internal/link/testdata)", dir != null && dir.isDirectory)
    val files = dir!!.listFiles { f -> f.name.endsWith(".json") }?.sortedBy { it.name }.orEmpty()
    assumeTrue("no *.json in ${dir.path}", files.isNotEmpty())
    return files.map { f -> JSONObject(f.readText()).also { if (!it.has("name")) it.put("name", f.nameWithoutExtension) } }
  }

  private fun JSONObject.str(vararg names: String): String? = names.firstNotNullOfOrNull { n -> optString(n).takeIf { it.isNotEmpty() } }

  private fun hubPub(c: JSONObject): ByteArray? = c.optJSONObject("keys")?.str("hub", "hub_pub")?.let(Codec::unb64u)

  @Test fun signedInputsAreRebuiltByteForByte() {
    var checked = 0
    for (c in cases()) {
      val f = c.optJSONObject("fields") ?: continue
      val want = c.optString("signed_hex").ifEmpty { null } ?: continue
      val name = c.getString("name")
      val got = when {
        name == "challenge" -> Frames.challengeInput(f.getString("hub"), f.getString("nonce"), f.getLong("exp"))
        name.startsWith("hello") -> Frames.helloInput(f.getString("hub"), f.getString("nonce"), f.getLong("exp"), f.getString("device"), f.getLong("last"))
        f.has("t") && f.has("seq") && !name.startsWith("bad-") -> {
          val payload = f.str("payload_hex")?.let(Codec::unhex) ?: Codec.unb64u(f.str("body", "payload")) ?: continue
          Frames.signedInput(f.getString("device"), f.getString("t"), f.getLong("seq"), f.getLong("exp"), f.optString("tag"), f.getString("ctx"), payload)
        }
        else -> continue
      }
      assertEquals("$name: signed input", want, Codec.hex(got))
      checked++
    }
    assumeTrue("no case with fields this runner knows", checked > 0)
  }

  @Test fun framesVerifyAndBadVariantsAreRejected() {
    var checked = 0
    for (c in cases()) {
      val name = c.getString("name")
      val frame = c.optJSONObject("frame") ?: continue
      val pub = hubPub(c) ?: continue
      if (frame.optString("t") == "challenge") {
        val got = Login.checkChallenge(frame, Codec.fingerprint(pub), TinkHubVerifier(pub))
        if (name.startsWith("bad-")) assertEquals("$name accepted", null, got) else assertNotNull("$name refused", got)
        checked++
        continue
      }
      if (frame.optString("t") == "hello") continue // the hub checks hellos (hello-phone is ECDSA, verified by Go)
      val f = c.optJSONObject("fields") ?: JSONObject()
      val device = f.str("device") ?: c.optJSONObject("keys")?.str("device") ?: continue
      val kv = MemoryKV()
      val store = Store(kv)
      store.configure("https://hub", device, listOf("wss://hub/v1/link"), 180, pub, Codec.fingerprint(pub), "qr")
      val keys = c.optJSONObject("keys")
      val agreement = keys?.str("ua_private", "box_private")?.let { d ->
        keys.str("ua_public", "box_pub", "box")?.let { SoftwareAgreement.fromScalar(Codec.unb64u(d)!!, Codec.unb64u(it)!!) }
      }
      val auth = keys?.str("auth")?.let(Codec::unb64u)
      val sink = RecordingSink()
      val p = FrameProcessor(store, TinkHubVerifier(pub), { b ->
        if (agreement == null || auth == null) throw CryptoException("no_key", "no phone key in this case")
        Rfc8291.decrypt(b, agreement, auth)
      }, sink, { 0L }) // the goldens' exp is in the past or future; clock 0 never expires them
      val out = p.onText(frame.toString(), f.str("ctx", "nonce") ?: "queue")
      if (name.startsWith("bad-")) {
        assertTrue("$name not rejected: ${out.note}", out.reject != null || (out.ack == null && out.ephemeral == null))
        assertEquals("$name moved last", 0L, store.last)
      } else if (Frames.isQueued(frame.optString("t"), frame.optLong("seq"))) {
        if (agreement == null) continue // a machine's HPKE frame: Go's case, not the phone's
        assertEquals("$name: ${out.note}", frame.getLong("seq"), out.ack)
      } else {
        assertNotNull("$name: ${out.note}", out.ephemeral)
      }
      checked++
    }
    assumeTrue("no frame case this runner knows", checked > 0)
  }

  @Test fun webpushGoldensOpenIfPresent() {
    val d = System.getProperty("wy.webpushVectors")?.let(::File)
    assumeTrue("no webpush testdata", d != null && d.isDirectory)
    var n = 0
    for (f in d!!.listFiles { x -> x.name.endsWith(".json") }.orEmpty()) {
      val o = JSONObject(f.readText())
      val priv = o.optString("ua_private").ifEmpty { null } ?: continue
      val ua = SoftwareAgreement.fromScalar(Codec.unb64u(priv)!!, Codec.unb64u(o.getString("ua_public"))!!)
      val plain = Rfc8291.decrypt(Codec.unb64u(o.getString("body"))!!, ua, Codec.unb64u(o.getString("auth"))!!)
      o.optString("plaintext").ifEmpty { null }?.let { assertEquals(f.name, it, String(plain, Charsets.UTF_8)) }
      n++
    }
    assumeTrue(n > 0)
  }
}
