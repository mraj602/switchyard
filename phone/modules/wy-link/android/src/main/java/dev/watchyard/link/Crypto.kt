package dev.watchyard.link

import com.google.crypto.tink.subtle.Ed25519Verify
import java.math.BigInteger
import java.security.AlgorithmParameters
import java.security.GeneralSecurityException
import java.security.KeyFactory
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

// The crypto of push.md 14.3 and 14.5 that does not depend on where the private keys live.
// The private-key operations are behind two small interfaces: production uses the Android
// Keystore (Keystore.kt), the JVM tests a software P-256 key, so RFC 8291's appendix runs
// unchanged in a unit test.

/** RFC 8291's key agreement key: `wy_link_agree` in the Keystore, a software key in tests. */
interface Agreement {
  /** The 65-byte SEC1 uncompressed public key (`p256dh`, the device's `box_pub`). */
  val publicKey: ByteArray

  /** ECDH with a peer's 65-byte uncompressed P-256 point; the 32-byte shared secret. */
  fun agree(peer: ByteArray): ByteArray
}

/** The login key: `wy_link_sign`, ECDSA P-256 SHA-256, DER signatures (keys.md 2). */
interface Signer {
  val publicKey: ByteArray

  fun sign(data: ByteArray): ByteArray
}

/** A hub signature check (Ed25519 over the pinned hub key). */
fun interface HubVerifier {
  fun verify(sig: ByteArray, data: ByteArray): Boolean
}

class CryptoException(val code: String, message: String) : Exception(message)

object P256 {
  val params: ECParameterSpec by lazy {
    val ap = AlgorithmParameters.getInstance("EC")
    ap.init(ECGenParameterSpec("secp256r1"))
    ap.getParameterSpec(ECParameterSpec::class.java)
  }

  /** 0x04 ‖ X ‖ Y, each coordinate 32 bytes big-endian. */
  fun encode(pub: ECPublicKey): ByteArray {
    val out = ByteArray(65)
    out[0] = 4
    fixed32(pub.w.affineX).copyInto(out, 1)
    fixed32(pub.w.affineY).copyInto(out, 33)
    return out
  }

  /** A 65-byte uncompressed point as a public key; the provider checks it is on the curve. */
  fun decode(b: ByteArray): ECPublicKey {
    if (b.size != 65 || b[0] != 4.toByte()) throw CryptoException("bad_point", "not an uncompressed P-256 point")
    val x = BigInteger(1, b.copyOfRange(1, 33))
    val y = BigInteger(1, b.copyOfRange(33, 65))
    val p = (params.curve.field as java.security.spec.ECFieldFp).p
    // y² = x³ + ax + b (mod p): refuse points off the curve before any key agreement.
    val lhs = y.modPow(BigInteger.valueOf(2), p)
    val rhs = x.modPow(BigInteger.valueOf(3), p).add(params.curve.a.multiply(x)).add(params.curve.b).mod(p)
    if (x >= p || y >= p || lhs != rhs) throw CryptoException("bad_point", "point not on P-256")
    return KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(ECPoint(x, y), params)) as ECPublicKey
  }

  private fun fixed32(n: BigInteger): ByteArray {
    val raw = n.toByteArray()
    return when {
      raw.size == 32 -> raw
      raw.size == 33 && raw[0] == 0.toByte() -> raw.copyOfRange(1, 33)
      raw.size < 32 -> ByteArray(32 - raw.size) + raw
      else -> throw CryptoException("bad_point", "coordinate too large")
    }
  }
}

/** HKDF-SHA256 (RFC 5869) from the platform's HMAC. */
object Hkdf {
  fun extract(salt: ByteArray, ikm: ByteArray): ByteArray = hmac(salt, ikm)

  fun expand(prk: ByteArray, info: ByteArray, len: Int): ByteArray {
    require(len <= 255 * 32)
    val out = ByteArray(len)
    var t = ByteArray(0)
    var pos = 0
    var i = 1
    while (pos < len) {
      t = hmac(prk, Codec.cat(t, info, byteArrayOf(i.toByte())))
      val n = minOf(t.size, len - pos)
      t.copyInto(out, pos, 0, n)
      pos += n
      i++
    }
    return out
  }

  private fun hmac(key: ByteArray, data: ByteArray): ByteArray {
    val m = Mac.getInstance("HmacSHA256")
    // An empty HMAC key is legal but SecretKeySpec refuses it: RFC 5869 uses a zero block then.
    m.init(SecretKeySpec(if (key.isEmpty()) ByteArray(32) else key, "HmacSHA256"))
    return m.doFinal(data)
  }
}

/**
 * RFC 8291 (Web Push message encryption) over RFC 8188 `aes128gcm`, one record: the reverse of
 * the hub's `webpush.Encrypt` (link.md 4.2). The shared secret lives only inside one call.
 */
object Rfc8291 {
  private const val HEADER = 16 + 4 + 1

  fun decrypt(body: ByteArray, agreement: Agreement, auth: ByteArray): ByteArray {
    if (auth.size != 16) throw CryptoException("bad_auth", "auth secret must be 16 bytes")
    if (body.size < HEADER + 65 + 17) throw CryptoException("bad_body", "body too short")
    val salt = body.copyOfRange(0, 16)
    val rs = ((body[16].toLong() and 0xff) shl 24) or ((body[17].toLong() and 0xff) shl 16) or
      ((body[18].toLong() and 0xff) shl 8) or (body[19].toLong() and 0xff)
    val idlen = body[20].toInt() and 0xff
    if (idlen != 65) throw CryptoException("bad_body", "keyid is not a P-256 point")
    if (rs < 18) throw CryptoException("bad_body", "record size too small")
    val asPublic = body.copyOfRange(HEADER, HEADER + 65)
    val ct = body.copyOfRange(HEADER + 65, body.size)
    // One record only: the hub never splits (link.md 4.2's 3 993-byte limit).
    if (ct.size > rs) throw CryptoException("bad_body", "more than one record")
    P256.decode(asPublic) // on the curve, before the private key is used
    val secret = agreement.agree(asPublic)
    try {
      val uaPublic = agreement.publicKey
      val keyInfo = Codec.cat(Codec.utf8("WebPush: info"), byteArrayOf(0), uaPublic, asPublic)
      val ikm = Hkdf.expand(Hkdf.extract(auth, secret), keyInfo, 32)
      val prk = Hkdf.extract(salt, ikm)
      val cek = Hkdf.expand(prk, Codec.cat(Codec.utf8("Content-Encoding: aes128gcm"), byteArrayOf(0)), 16)
      val nonce = Hkdf.expand(prk, Codec.cat(Codec.utf8("Content-Encoding: nonce"), byteArrayOf(0)), 12)
      val c = Cipher.getInstance("AES/GCM/NoPadding")
      c.init(Cipher.DECRYPT_MODE, SecretKeySpec(cek, "AES"), GCMParameterSpec(128, nonce))
      val record = try {
        c.doFinal(ct)
      } catch (e: GeneralSecurityException) {
        throw CryptoException("bad_body", "record does not open")
      }
      // RFC 8188 2: padding is zeros after a delimiter, 0x02 for the last record.
      var end = record.size - 1
      while (end >= 0 && record[end] == 0.toByte()) end--
      if (end < 0 || record[end] != 2.toByte()) throw CryptoException("bad_body", "no last-record delimiter")
      return record.copyOfRange(0, end)
    } finally {
      secret.fill(0)
    }
  }
}

/** Ed25519 verification of the pinned hub key through Tink (push.md 14.1), on every API level. */
class TinkHubVerifier(pub: ByteArray) : HubVerifier {
  private val v: Ed25519Verify

  init {
    if (pub.size != 32) throw CryptoException("bad_hub_key", "hub key must be 32 bytes")
    v = Ed25519Verify(pub)
  }

  override fun verify(sig: ByteArray, data: ByteArray): Boolean =
    try {
      if (sig.size != 64) false else {
        v.verify(sig, data)
        true
      }
    } catch (_: GeneralSecurityException) {
      false
    }
}

/** keys.md 7.1: the claim's `keys` JSON sealed with the QR's `k`. */
object ClaimSeal {
  fun seal(k: ByteArray, secret: String, plaintext: ByteArray, nonce: ByteArray): String {
    if (k.size != 16) throw CryptoException("bad_seal_key", "k must be 16 bytes")
    if (nonce.size != 12) throw CryptoException("bad_nonce", "nonce must be 12 bytes")
    val c = Cipher.getInstance("AES/GCM/NoPadding")
    c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(k, "AES"), GCMParameterSpec(128, nonce))
    c.updateAAD(Codec.utf8("wy-reg-v1\n$secret")) // the hub opens it with this AAD (keys.md 7.1)
    return Codec.b64u(Codec.cat(nonce, c.doFinal(plaintext)))
  }
}

/** keys.md 4.1: a document signature is Ed25519 over "wy-doc-v1\n" ‖ type ‖ "\n" ‖ body bytes. */
object Docs {
  fun signedInput(type: String, body: ByteArray): ByteArray = Codec.cat(Codec.utf8("wy-doc-v1\n$type\n"), body)
}
