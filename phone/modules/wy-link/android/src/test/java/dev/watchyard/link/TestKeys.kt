package dev.watchyard.link

import com.google.crypto.tink.subtle.Ed25519Sign
import org.json.JSONObject
import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.PrivateKey
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPrivateKeySpec
import javax.crypto.Cipher
import javax.crypto.KeyAgreement
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

// Software stand-ins for the Keystore keys and the hub key, for the JVM tests only.

/** The store's KV in a map. */
class MemoryKV : KV {
  val map = HashMap<String, Any?>()

  @Synchronized override fun getString(k: String): String? = map[k] as String?
  @Synchronized override fun getLong(k: String, def: Long): Long = (map[k] as Long?) ?: def
  @Synchronized override fun getBoolean(k: String, def: Boolean): Boolean = (map[k] as Boolean?) ?: def
  @Synchronized override fun put(values: Map<String, Any?>, commit: Boolean) {
    for ((k, v) in values) if (v == null) map.remove(k) else map[k] = v
  }
  @Synchronized override fun clear() = map.clear()
}

fun Codec.unhex(s: String): ByteArray {
  require(s.length % 2 == 0) { "odd hex" }
  return ByteArray(s.length / 2) { i -> s.substring(2 * i, 2 * i + 2).toInt(16).toByte() }
}

/** Bounds of the n-th backoff delay (0-based). */
fun Backoff.Companion.bounds(n: Int): LongRange {
  val base = STEPS_S[minOf(n, STEPS_S.size - 1)] * 1000L
  return (base * (1 - JITTER)).toLong()..(base * (1 + JITTER)).toLong()
}

/** The hub's side of keys.md 7.1's seal. */
fun ClaimSeal.open(k: ByteArray, secret: String, sealed: String): ByteArray {
  val b = Codec.unb64u(sealed)!!
  val c = Cipher.getInstance("AES/GCM/NoPadding")
  c.init(Cipher.DECRYPT_MODE, SecretKeySpec(k, "AES"), GCMParameterSpec(128, b.copyOfRange(0, 12)))
  c.updateAAD(Codec.utf8("wy-reg-v1\n$secret"))
  return c.doFinal(b, 12, b.size - 12)
}

/** A P-256 key agreement key in memory, from a raw scalar (RFC 8291's ua_private) or fresh. */
class SoftwareAgreement private constructor(private val priv: PrivateKey, override val publicKey: ByteArray) : Agreement {
  override fun agree(peer: ByteArray): ByteArray {
    val ka = KeyAgreement.getInstance("ECDH")
    ka.init(priv)
    ka.doPhase(P256.decode(peer), true)
    return ka.generateSecret()
  }

  companion object {
    fun fromScalar(d: ByteArray, pub: ByteArray): SoftwareAgreement {
      val priv = KeyFactory.getInstance("EC").generatePrivate(ECPrivateKeySpec(BigInteger(1, d), P256.params))
      return SoftwareAgreement(priv, pub)
    }

    fun fresh(): SoftwareAgreement {
      val g = KeyPairGenerator.getInstance("EC")
      g.initialize(ECGenParameterSpec("secp256r1"))
      val kp = g.generateKeyPair()
      return SoftwareAgreement(kp.private, P256.encode(kp.public as ECPublicKey))
    }
  }
}

/** The login key in memory: ECDSA P-256 SHA-256, DER, as the Keystore's. */
class SoftwareSigner : Signer {
  private val kp = KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }.generateKeyPair()
  override val publicKey: ByteArray = P256.encode(kp.public as ECPublicKey)

  override fun sign(data: ByteArray): ByteArray = Signature.getInstance("SHA256withECDSA").run {
    initSign(kp.private)
    update(data)
    sign()
  }

  fun verify(sig: ByteArray, data: ByteArray): Boolean = Signature.getInstance("SHA256withECDSA").run {
    initVerify(kp.public)
    update(data)
    verify(sig)
  }
}

/** The hub's Ed25519 key from a seed (link.md 10: the hub seed is 0x01…01). */
class HubKey(seed: ByteArray = ByteArray(32) { 1 }) {
  private val signer = Ed25519Sign(seed)
  val pub: ByteArray = Ed25519Sign.KeyPair.newKeyPairFromSeed(seed).publicKey
  val fp: String = Codec.fingerprint(pub)

  fun sign(data: ByteArray): ByteArray = signer.sign(data)

  fun verifier(): HubVerifier = TinkHubVerifier(pub)

  /** A hub→device frame as link.md 4.3 signs it. */
  fun frame(device: String, t: String, seq: Long, exp: Long, tag: String, body: ByteArray, ctx: String = Frames.QUEUE_CTX): String {
    val sig = sign(Frames.signedInput(device, t, seq, exp, tag, ctx, body))
    return JSONObject().put("t", t).put("seq", seq).put("exp", exp).put("tag", tag)
      .put("body", Codec.b64u(body)).put("sig", Codec.b64u(sig)).toString()
  }

  fun challenge(nonce: String, exp: Long): JSONObject = JSONObject()
    .put("t", "challenge").put("v", 1).put("hub", fp).put("nonce", nonce).put("exp", exp)
    .put("sig", Codec.b64u(sign(Frames.challengeInput(fp, nonce, exp))))
}

/** The hub's RFC 8291 encryption (webpush.Encrypt), for sealing test bodies. */
object WebPushSeal {
  fun encrypt(plain: ByteArray, uaPublic: ByteArray, auth: ByteArray, sender: SoftwareAgreement = SoftwareAgreement.fresh(), salt: ByteArray = ByteArray(16) { 7 }, pad: Int = 128): ByteArray {
    val secret = sender.agree(uaPublic)
    val asPub = sender.publicKey
    val ikm = Hkdf.expand(Hkdf.extract(auth, secret), Codec.cat(Codec.utf8("WebPush: info"), byteArrayOf(0), uaPublic, asPub), 32)
    val prk = Hkdf.extract(salt, ikm)
    val cek = Hkdf.expand(prk, Codec.cat(Codec.utf8("Content-Encoding: aes128gcm"), byteArrayOf(0)), 16)
    val nonce = Hkdf.expand(prk, Codec.cat(Codec.utf8("Content-Encoding: nonce"), byteArrayOf(0)), 12)
    var record = plain + byteArrayOf(2)
    if (pad > 0) record += ByteArray((record.size + pad - 1) / pad * pad - record.size)
    val c = Cipher.getInstance("AES/GCM/NoPadding")
    c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(cek, "AES"), GCMParameterSpec(128, nonce))
    val header = salt + byteArrayOf(0, 0, 0x10, 0) + byteArrayOf(65) + asPub
    return header + c.doFinal(record)
  }
}

/** A recording sink. */
class RecordingSink : Sink {
  val posted = ArrayList<AlertNote>()
  val dismissed = ArrayList<String>()
  val refreshed = ArrayList<String>()
  var missed = 0

  override fun post(n: AlertNote) {
    posted += n
  }
  override fun dismiss(tag: String) {
    dismissed += tag
  }
  override fun refresh(json: String) {
    refreshed += json
  }
  override fun missed() {
    missed++
  }
}

/** A phone with its keys, a store and a processor, the hub key pinned. */
class TestPhone(val kv: MemoryKV = MemoryKV(), val hub: HubKey = HubKey(), var now: Long = 1_760_518_800_000L) {
  val device = "dev_01JA4TESTPHONE"
  val agreement = SoftwareAgreement.fresh()
  val auth = ByteArray(16) { (it * 3).toByte() }
  val store = Store(kv)
  val sink = RecordingSink()

  init {
    if (!store.configured) {
      store.saveKeys(auth)
      store.configure("https://hub.example.ts.net", device, listOf("wss://hub.example.ts.net/v1/link"), 180, hub.pub, hub.fp, "qr")
    }
  }

  fun processor(sink: Sink = this.sink) = FrameProcessor(store, hub.verifier(), { Rfc8291.decrypt(it, agreement, auth) }, sink, { now })

  fun seal(json: String) = WebPushSeal.encrypt(Codec.utf8(json), agreement.publicKey, auth)

  val nowS get() = now / 1000
}
