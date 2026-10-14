package dev.switchyard.link

import org.json.JSONArray
import org.json.JSONException
import org.json.JSONObject

/**
 * The pure half of the updater (phone.md P16): manifest parsing, version and build compare, the
 * GitHub release filter and the signing-certificate decision. No android.* here, so the JVM
 * tests run it as is; `UpdateInstaller` supplies the PackageManager facts through
 * [SigningSource].
 */
data class Manifest(
  val version: String,
  val build: Long,
  val minSdk: Int,
  val apk: String,
  val size: Long,
  val sha256: String,
  val certSha256: String,
  val notes: String,
)

/** What the JS side sees (UpdateInfo in SyLink.types.ts). */
data class UpdateOffer(val manifest: Manifest, val url: String, val source: String) {
  fun toMap(): Map<String, Any?> = mapOf(
    "version" to manifest.version, "build" to manifest.build, "url" to url, "sha256" to manifest.sha256,
    "certSha256" to manifest.certSha256, "size" to manifest.size, "notes" to manifest.notes, "source" to source,
  )

  companion object {
    /** Reads back what the JS side passes to installUpdate; null when a field is missing or malformed. */
    fun fromMap(m: Map<String, Any?>): UpdateOffer? {
      val version = m["version"] as? String ?: return null
      val url = m["url"] as? String ?: return null
      val sha = (m["sha256"] as? String)?.lowercase() ?: return null
      val cert = (m["certSha256"] as? String)?.lowercase() ?: return null
      val build = (m["build"] as? Number)?.toLong() ?: return null
      val source = m["source"] as? String ?: return null
      if (!Regex("^[0-9a-f]{64}$").matches(sha) || !Regex("^[0-9a-f]{64}$").matches(cert) || build <= 0 ||
        Updates.parseVersion(version) == null || (source != "hub" && source != "github") || url.isEmpty()
      ) return null
      val manifest = Manifest(version, build, 0, "", (m["size"] as? Number)?.toLong() ?: 0, sha, cert, m["notes"] as? String ?: "")
      return UpdateOffer(manifest, url, source)
    }
  }
}

/** One GitHub release after the filter: its tag and asset download URLs by name. */
data class GhRelease(val tag: String, val assets: Map<String, String>)

object Updates {
  private val HEX64 = Regex("^[0-9a-f]{64}$")
  private val FILE = Regex("^[A-Za-z0-9][A-Za-z0-9._-]*$")
  private val TAG = Regex("^phone-v(\\d+)\\.(\\d+)\\.(\\d+)$")

  /** The same checks as the hub mirror's ParseManifest (internal/hub/appmirror). Null when unusable. */
  fun parseManifest(json: String): Manifest? = try {
    val o = JSONObject(json)
    val m = Manifest(
      version = o.getString("version"),
      build = o.getLong("build"),
      minSdk = o.optInt("min_sdk", 0),
      apk = o.getString("apk"),
      size = o.optLong("size", 0),
      sha256 = o.getString("sha256"),
      certSha256 = o.getString("cert_sha256"),
      notes = o.optString("notes", ""),
    )
    if (o.optInt("v", 0) != 1 || m.version.isEmpty() || m.build <= 0 || !FILE.matches(m.apk) ||
      !HEX64.matches(m.sha256) || !HEX64.matches(m.certSha256) || parseVersion(m.version) == null
    ) null else m
  } catch (_: JSONException) {
    null
  }

  /** "1.2.3" to [1, 2, 3]; null for anything else. */
  fun parseVersion(v: String): List<Int>? {
    val parts = v.split('.')
    if (parts.size != 3) return null
    val n = parts.map { it.toIntOrNull() ?: return null }
    return if (n.any { it < 0 }) null else n
  }

  /** Numeric compare of two X.Y.Z versions (0.10.0 is newer than 0.9.0); unparsable versions sort lowest. */
  fun compareVersions(a: String, b: String): Int {
    val x = parseVersion(a)
    val y = parseVersion(b)
    if (x == null || y == null) return if (x == null && y == null) 0 else if (x == null) -1 else 1
    for (i in 0 until 3) if (x[i] != y[i]) return x[i].compareTo(y[i])
    return 0
  }

  /** P16.3: offered when `build` is higher than the installed versionCode and min_sdk fits this phone. */
  fun isOffered(m: Manifest, installedBuild: Long, sdk: Int): Boolean = m.build > installedBuild && m.minSdk <= sdk

  /**
   * The newest non-draft, non-prerelease release tagged phone-vX.Y.Z (P16.2), by version
   * number rather than list order. Null when the JSON is not a release list or none matches.
   */
  fun newestRelease(releasesJson: String): GhRelease? {
    val arr = try {
      JSONArray(releasesJson)
    } catch (_: JSONException) {
      return null
    }
    var best: GhRelease? = null
    var bestV: List<Int>? = null
    for (i in 0 until arr.length()) {
      val r = arr.optJSONObject(i) ?: continue
      if (r.optBoolean("draft") || r.optBoolean("prerelease")) continue
      val tag = r.optString("tag_name")
      val g = TAG.matchEntire(tag) ?: continue
      val v = g.groupValues.drop(1).map { it.toInt() }
      if (bestV == null || compareVersions(v.joinToString("."), bestV.joinToString(".")) > 0) {
        val assets = HashMap<String, String>()
        val list = r.optJSONArray("assets") ?: JSONArray()
        for (j in 0 until list.length()) {
          val a = list.optJSONObject(j) ?: continue
          assets[a.optString("name")] = a.optString("browser_download_url")
        }
        best = GhRelease(tag, assets)
        bestV = v
      }
    }
    return best
  }

  /** The hub's answer: the manifest plus the relative `url`; resolved against the pinned origin. */
  fun hubOffer(origin: String, body: String): Pair<Manifest, String>? {
    val m = parseManifest(body) ?: return null
    val rel = try {
      JSONObject(body).optString("url")
    } catch (_: JSONException) {
      return null
    }
    if (!rel.startsWith("/v1/app/android")) return null
    return m to origin.trimEnd('/') + rel
  }

  /** Whether the hub's bearer token may go with this request: only to the paired hub's own origin. */
  fun sendsToken(url: String, origin: String?): Boolean =
    origin != null && (url == origin.trimEnd('/') || url.startsWith(origin.trimEnd('/') + "/"))
}

/** An APK's package facts: its name, versionCode and signer certificates (DER). */
class ArchiveFacts(val packageName: String, val versionCode: Long, val signers: List<ByteArray>)

/** The PackageManager, abstracted (JVM tests use a fake). */
interface SigningSource {
  /** `getPackageArchiveInfo(path, GET_SIGNING_CERTIFICATES)`; null when the file is not a readable APK. */
  fun archive(path: String): ArchiveFacts?

  /** The installed app's signer certificates and versionCode; null when unknown. */
  fun installed(): ArchiveFacts?
}

enum class Verdict { OK, NOT_AN_APK, WRONG_PACKAGE, NOT_NEWER, UNSIGNED, MANIFEST_CERT_MISMATCH, INSTALLED_CERT_MISMATCH }

object CertCheck {
  /** SHA-256 of each signer certificate as lower-case hex, sorted. */
  fun fingerprints(signers: List<ByteArray>): List<String> = signers.map { Codec.hex(Codec.sha256(it)) }.sorted()

  /**
   * P16.3: the APK's signing certificate must equal BOTH the manifest's `cert_sha256` and the
   * installed app's own, and it must be this package at a higher versionCode. A multi-signer APK
   * matches only when its whole set is the single manifest certificate.
   */
  fun verify(src: SigningSource, apkPath: String, manifestCertSha256: String): Verdict {
    val a = src.archive(apkPath) ?: return Verdict.NOT_AN_APK
    val me = src.installed() ?: return Verdict.INSTALLED_CERT_MISMATCH
    if (a.packageName != me.packageName) return Verdict.WRONG_PACKAGE
    if (a.versionCode <= me.versionCode) return Verdict.NOT_NEWER
    val got = fingerprints(a.signers)
    if (got.isEmpty()) return Verdict.UNSIGNED
    if (got != listOf(manifestCertSha256.lowercase())) return Verdict.MANIFEST_CERT_MISMATCH
    if (fingerprints(me.signers) != got) return Verdict.INSTALLED_CERT_MISMATCH
    return Verdict.OK
  }
}
