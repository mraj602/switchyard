package dev.watchyard.link

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.SecureRandom
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import javax.crypto.KeyAgreement

/**
 * push.md 14.3: the two non-exportable Keystore keys. StrongBox when present, retried without
 * it; no user authentication, so the service works while the phone is locked after its first
 * unlock. Key agreement in the Keystore needs API 31 (the reason for minSdk 31).
 */
object Keystore {
  const val SIGN = "sy_link_sign"
  const val AGREE = "sy_link_agree"
  private const val PROVIDER = "AndroidKeyStore"

  private fun ks(): KeyStore = KeyStore.getInstance(PROVIDER).apply { load(null) }

  fun has(): Boolean = ks().let { it.containsAlias(SIGN) && it.containsAlias(AGREE) }

  /** Makes both keys (replacing old ones). */
  fun create() {
    delete()
    generate(SIGN, KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY)
    generate(AGREE, KeyProperties.PURPOSE_AGREE_KEY)
    // A StrongBox that made the key but cannot use it for ECDH is replaced by a TEE key.
    if (!selfTestAgree()) {
      ks().deleteEntry(AGREE)
      generate(AGREE, KeyProperties.PURPOSE_AGREE_KEY, strongBox = false)
    }
  }

  fun delete() {
    val k = ks()
    for (a in listOf(SIGN, AGREE)) if (k.containsAlias(a)) k.deleteEntry(a)
  }

  private fun generate(alias: String, purposes: Int, strongBox: Boolean = true) {
    fun spec(sb: Boolean) = KeyGenParameterSpec.Builder(alias, purposes)
      .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
      .apply { if (purposes and KeyProperties.PURPOSE_SIGN != 0) setDigests(KeyProperties.DIGEST_SHA256) }
      .setUserAuthenticationRequired(false)
      .setIsStrongBoxBacked(sb)
      .build()
    val g = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, PROVIDER)
    if (strongBox) {
      try {
        g.initialize(spec(true))
        g.generateKeyPair()
        return
      } catch (_: StrongBoxUnavailableException) {
        // no StrongBox on this phone: the TEE below
      } catch (_: java.security.ProviderException) {
        // some StrongBox implementations throw this instead
      }
    }
    g.initialize(spec(false))
    g.generateKeyPair()
  }

  private fun selfTestAgree(): Boolean = try {
    val g = KeyPairGenerator.getInstance("EC")
    g.initialize(ECGenParameterSpec("secp256r1"), SecureRandom())
    val peer = g.generateKeyPair().public as ECPublicKey
    agreement().agree(P256.encode(peer)).size == 32
  } catch (_: Exception) {
    false
  }

  fun signer(): Signer = object : Signer {
    override val publicKey: ByteArray get() = P256.encode(ks().getCertificate(SIGN).publicKey as ECPublicKey)

    override fun sign(data: ByteArray): ByteArray {
      val s = Signature.getInstance("SHA256withECDSA")
      s.initSign(ks().getKey(SIGN, null) as PrivateKey)
      s.update(data)
      return s.sign() // ASN.1 DER (keys.md 2)
    }
  }

  fun agreement(): Agreement = object : Agreement {
    override val publicKey: ByteArray get() = P256.encode(ks().getCertificate(AGREE).publicKey as ECPublicKey)

    override fun agree(peer: ByteArray): ByteArray {
      val ka = KeyAgreement.getInstance("ECDH", PROVIDER)
      ka.init(ks().getKey(AGREE, null) as PrivateKey)
      ka.doPhase(P256.decode(peer), true)
      return ka.generateSecret()
    }
  }
}
