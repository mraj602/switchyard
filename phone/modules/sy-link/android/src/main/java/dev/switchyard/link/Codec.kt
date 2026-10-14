package dev.switchyard.link

import java.security.MessageDigest
import java.util.Base64

// Encodings of keys.md 2 and the byte strings of link.md 1. Pure JVM (no android.*), so the
// JVM tests run it as is.
object Codec {
  private val b64e = Base64.getUrlEncoder().withoutPadding()
  private val b64d = Base64.getUrlDecoder()

  /** base64url without padding. */
  fun b64u(b: ByteArray): String = b64e.encodeToString(b)

  /** Decodes base64url, padding tolerated; null when it is not base64url. */
  fun unb64u(s: String?): ByteArray? {
    if (s == null) return null
    return try {
      b64d.decode(s.trimEnd('='))
    } catch (_: IllegalArgumentException) {
      null
    }
  }

  fun sha256(b: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(b)

  fun utf8(s: String): ByteArray = s.toByteArray(Charsets.UTF_8)

  fun hex(b: ByteArray): String = b.joinToString("") { "%02x".format(it) }

  fun unhex(s: String): ByteArray {
    require(s.length % 2 == 0) { "odd hex" }
    return ByteArray(s.length / 2) { i -> s.substring(2 * i, 2 * i + 2).toInt(16).toByte() }
  }

  private const val B32 = "abcdefghijklmnopqrstuvwxyz234567"

  /** RFC 4648 base32, lower case, no padding. */
  fun base32(b: ByteArray): String {
    val out = StringBuilder()
    var buf = 0
    var bits = 0
    for (x in b) {
      buf = (buf shl 8) or (x.toInt() and 0xff)
      bits += 8
      while (bits >= 5) {
        out.append(B32[(buf shr (bits - 5)) and 31])
        bits -= 5
      }
    }
    if (bits > 0) out.append(B32[(buf shl (5 - bits)) and 31])
    return out.toString()
  }

  /** keys.md 2: lower(base32(SHA-256(pub)[0:15])), 24 characters. */
  fun fingerprint(pub: ByteArray): String = base32(sha256(pub).copyOfRange(0, 15))

  /** Concatenates byte strings ("‖"). */
  fun cat(vararg parts: ByteArray): ByteArray {
    val out = ByteArray(parts.sumOf { it.size })
    var i = 0
    for (p in parts) {
      System.arraycopy(p, 0, out, i, p.size)
      i += p.size
    }
    return out
  }

  /** Text pieces joined with "\n" and UTF-8 encoded, the shape of every signed input. */
  fun lines(vararg parts: String): ByteArray = utf8(parts.joinToString("\n"))

  /** link.md 1: decimal(n), base-10 ASCII, no sign, padding or leading zeros. */
  fun decimal(n: Long): String {
    require(n >= 0) { "negative" }
    return n.toString()
  }
}
