package dev.watchyard.link

import org.json.JSONArray
import org.json.JSONObject

/**
 * keys.md 7.3's checks of the claim reply, done natively because the app's JavaScript has no
 * Ed25519 (phone.md P2): the hub key's fingerprint (and the QR's `h=` when there was one), the
 * certificate's hub signature, that it holds this phone's own keys, and `reg = qr` after a
 * sealed claim. Pure: the tests build replies with a software hub key.
 */
object Pairing {
  class Refused(val code: String, message: String) : Exception(message)

  /** The JSON of the claim's `keys` (keys.md 7.1) for these public keys. */
  fun keysJson(signPub: ByteArray, boxPub: ByteArray, auth: ByteArray): JSONObject = JSONObject()
    .put("sign", JSONObject().put("alg", "p256").put("pub", Codec.b64u(signPub)))
    .put("box", JSONObject().put("alg", "p256").put("pub", Codec.b64u(boxPub)))
    .put("auth", Codec.b64u(auth))

  /**
   * Throws [Refused] when the reply cannot be trusted:
   * - `hub_key_mismatch`: `fp` is not the fingerprint of `pub`, or not the QR's `h=`;
   * - `tampered`: the certificate is missing, not signed by the hub key, not for this device and
   *   hub, does not hold this phone's keys, or `reg` is not `qr` after a sealed claim.
   */
  fun check(
    hubPub: ByteArray,
    hubFp: String,
    qrFp: String?,
    device: String,
    reg: String,
    cert: JSONObject?,
    signPub: ByteArray,
    boxPub: ByteArray,
  ) {
    if (hubPub.size != 32 || Codec.fingerprint(hubPub) != hubFp) throw Refused("hub_key_mismatch", "hub key and fingerprint differ")
    if (qrFp != null && qrFp != hubFp) throw Refused("hub_key_mismatch", "This hub's key does not match the QR code")
    if (reg != "qr" && reg != "tofu") throw Refused("tampered", "unknown reg")
    if (qrFp != null && reg != "qr") throw Refused("tampered", "a sealed claim came back as tofu")
    if (cert == null) throw Refused("tampered", "no certificate")
    if (cert.optString("type") != "device_cert") throw Refused("tampered", "not a device certificate")
    val body = Codec.unb64u(cert.optString("body")) ?: throw Refused("tampered", "certificate body")
    val sigs: JSONArray = cert.optJSONArray("sigs") ?: throw Refused("tampered", "certificate signatures")
    // keys.md 4.1: exactly the signatures the type names, each by a distinct `by`. A phone's
    // certificate has the hub's; an owner signature may be added later (4.2) and is not ours to check.
    val by = HashSet<String>()
    var hubOk = false
    val verifier = TinkHubVerifier(hubPub)
    for (i in 0 until sigs.length()) {
      val s = sigs.optJSONObject(i) ?: throw Refused("tampered", "certificate signature")
      val who = s.optString("by")
      if (!by.add(who) || who !in setOf("hub", "owner")) throw Refused("tampered", "unexpected signature")
      if (who == "hub") {
        val sig = Codec.unb64u(s.optString("sig")) ?: throw Refused("tampered", "certificate signature")
        hubOk = verifier.verify(sig, Docs.signedInput("device_cert", body))
      }
    }
    if (!hubOk) throw Refused("tampered", "certificate not signed by the hub key")
    val c = try {
      JSONObject(String(body, Charsets.UTF_8))
    } catch (_: Exception) {
      throw Refused("tampered", "certificate body")
    }
    val ok = c.optInt("v", 0) == 1 && c.optString("device") == device && c.optString("kind") == "phone" &&
      c.optString("hub") == hubFp && c.optString("reg") == reg &&
      c.optJSONObject("sign")?.let { it.optString("alg") == "p256" && it.optString("pub") == Codec.b64u(signPub) } == true &&
      c.optJSONObject("box")?.let { it.optString("alg") == "p256" && it.optString("pub") == Codec.b64u(boxPub) } == true
    if (!ok) throw Refused("tampered", "certificate does not hold this phone's keys")
  }
}
