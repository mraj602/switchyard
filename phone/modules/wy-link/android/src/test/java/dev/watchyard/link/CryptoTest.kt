package dev.watchyard.link

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class CryptoTest {
  // RFC 8291 section 5 / appendix A (the same constants as internal/hub/push/webpush/encrypt_test.go).
  private val plain = "When I grow up, I want to be a watermelon"
  private val uaPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
  private val uaPublic = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
  private val auth = "BTBZMqHH6r4Tts7J_aSIgg"
  private val asPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
  private val salt = "DGv6ra1nlYgDCS1FRnbzlw"
  private val body = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
    "mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
    "pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"

  private fun b(s: String) = Codec.unb64u(s)!!
  private val ua get() = SoftwareAgreement.fromScalar(b(uaPrivate), b(uaPublic))

  @Test fun rfc8291AppendixDecrypts() {
    assertEquals(plain, String(Rfc8291.decrypt(b(body), ua, b(auth)), Charsets.UTF_8))
  }

  @Test fun rfc8291EncryptHelperIsByteExact() {
    // the test-side seal reproduces the RFC record with the appendix's sender key and salt
    val asPub = b(body).copyOfRange(21, 86)
    val sender = SoftwareAgreement.fromScalar(b(asPrivate), asPub)
    val sealed = WebPushSeal.encrypt(Codec.utf8(plain), b(uaPublic), b(auth), sender, b(salt), pad = 0)
    assertEquals(body, Codec.b64u(sealed))
  }

  @Test fun rfc8291RefusesTamperingAndWrongKeys() {
    val good = b(body)
    for (i in listOf(0, 20, 30, 90, good.size - 1)) {
      val bad = good.copyOf()
      bad[i] = (bad[i].toInt() xor 1).toByte()
      try {
        Rfc8291.decrypt(bad, ua, b(auth))
        fail("byte $i changed and it still opened")
      } catch (_: CryptoException) {
      } catch (_: java.security.GeneralSecurityException) {
      }
    }
    try {
      Rfc8291.decrypt(good, ua, ByteArray(16))
      fail("wrong auth opened")
    } catch (_: CryptoException) {
    }
    try {
      Rfc8291.decrypt(good, SoftwareAgreement.fresh(), b(auth))
      fail("wrong key opened")
    } catch (_: CryptoException) {
    }
  }

  @Test fun rfc8291RoundTripWithPadding() {
    val me = SoftwareAgreement.fresh()
    val a = ByteArray(16) { 9 }
    val text = "{\"v\":1,\"type\":\"alert\"}"
    val sealed = WebPushSeal.encrypt(Codec.utf8(text), me.publicKey, a, pad = 128)
    assertEquals(0, (sealed.size - 86 - 16) % 128)
    assertEquals(text, String(Rfc8291.decrypt(sealed, me, a), Charsets.UTF_8))
  }

  @Test fun pointsOffTheCurveAreRefused() {
    val p = b(uaPublic).copyOf()
    p[64] = (p[64].toInt() xor 1).toByte()
    try {
      P256.decode(p)
      fail("off-curve point accepted")
    } catch (_: CryptoException) {
    }
    assertArrayEquals(b(uaPublic), P256.encode(P256.decode(b(uaPublic))))
  }

  @Test fun ed25519Rfc8032VectorsThroughTink() {
    // RFC 8032 7.1, TEST 1 (empty message) and TEST 2 (one byte 0x72)
    val v1 = TinkHubVerifier(Codec.unhex("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"))
    val s1 = Codec.unhex(
      "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",
    )
    assertTrue(v1.verify(s1, ByteArray(0)))
    assertFalse(v1.verify(s1, byteArrayOf(0)))
    val v2 = TinkHubVerifier(Codec.unhex("3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"))
    val s2 = Codec.unhex(
      "92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00",
    )
    assertTrue(v2.verify(s2, byteArrayOf(0x72)))
    val bad = s2.copyOf()
    bad[0] = (bad[0].toInt() xor 1).toByte()
    assertFalse(v2.verify(bad, byteArrayOf(0x72)))
    assertFalse(v2.verify(s2.copyOf(63), byteArrayOf(0x72)))
  }

  @Test fun fingerprintAndBase32() {
    // RFC 4648 10
    assertEquals("my", Codec.base32(Codec.utf8("f")))
    assertEquals("mzxw6ytboi", Codec.base32(Codec.utf8("foobar")))
    // keys.md 2: lower(base32(SHA-256(pub)[0:15])), 24 characters (python hashlib reference)
    val fp = Codec.fingerprint(ByteArray(32) { it.toByte() })
    assertEquals("mmg42klgyqzwneiskrelxms3", fp)
    assertEquals(24, fp.length)
  }

  @Test fun hkdfRfc5869Case1() {
    val ikm = Codec.unhex("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")
    val salt = Codec.unhex("000102030405060708090a0b0c")
    val info = Codec.unhex("f0f1f2f3f4f5f6f7f8f9")
    val okm = Hkdf.expand(Hkdf.extract(salt, ikm), info, 42)
    assertEquals("3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865", Codec.hex(okm))
  }

  @Test fun claimSealOpensWithTheRightCodeOnly() {
    val k = ByteArray(16) { 5 }
    val sealed = ClaimSeal.seal(k, "secret22charsxxxxxxxxx", Codec.utf8("{\"a\":1}"), ByteArray(12) { 1 })
    assertEquals("{\"a\":1}", String(ClaimSeal.open(k, "secret22charsxxxxxxxxx", sealed), Charsets.UTF_8))
    try {
      ClaimSeal.open(k, "another-secret", sealed)
      fail("AAD not bound")
    } catch (_: java.security.GeneralSecurityException) {
    }
  }
}
