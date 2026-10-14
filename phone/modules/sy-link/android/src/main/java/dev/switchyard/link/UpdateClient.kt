package dev.switchyard.link

import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/** A failed update step; the message is safe to show and never holds a token. */
class UpdateException(message: String) : Exception(message)

/** An HTTP answer, or null `body` when the host could not be reached. */
class Fetched(val status: Int, val body: String?)

/** The network, abstracted so the JVM tests script the hub and GitHub. */
interface Fetcher {
  /** GET with an optional bearer token. A network failure is `Fetched(0, null)`. */
  fun get(url: String, token: String?): Fetched

  /** Streams the body to [dest] and returns its SHA-256 as hex; null on any failure or when over [maxBytes]. */
  fun download(url: String, token: String?, dest: File, maxBytes: Long): String?
}

class OkFetcher(
  private val client: OkHttpClient = OkHttpClient.Builder()
    .connectTimeout(15, TimeUnit.SECONDS)
    .readTimeout(60, TimeUnit.SECONDS)
    .followSslRedirects(false) // https stays https; GitHub's asset hosts redirect within https
    .build(),
) : Fetcher {
  private fun req(url: String, token: String?): Request {
    val b = Request.Builder().url(url).header("User-Agent", "switchyard-app").header("Accept", "application/json")
    if (token != null) b.header("Authorization", "Bearer $token")
    return b.build()
  }

  override fun get(url: String, token: String?): Fetched = try {
    client.newCall(req(url, token)).execute().use { r ->
      val body = r.body?.source()?.let { s -> s.request(MAX_JSON + 1); if (s.buffer.size > MAX_JSON) null else s.readUtf8() }
      Fetched(r.code, body)
    }
  } catch (_: IOException) {
    Fetched(0, null)
  } catch (_: IllegalArgumentException) {
    Fetched(0, null)
  }

  override fun download(url: String, token: String?, dest: File, maxBytes: Long): String? = try {
    client.newCall(req(url, token)).execute().use { r ->
      val src = r.body?.byteStream()
      if (!r.isSuccessful || src == null) return null
      val md = MessageDigest.getInstance("SHA-256")
      var n = 0L
      dest.outputStream().use { out ->
        val buf = ByteArray(64 * 1024)
        while (true) {
          val k = src.read(buf)
          if (k < 0) break
          n += k
          if (n > maxBytes) return null
          md.update(buf, 0, k)
          out.write(buf, 0, k)
        }
      }
      Codec.hex(md.digest())
    }
  } catch (_: IOException) {
    null
  } catch (_: IllegalArgumentException) {
    null
  }

  companion object {
    const val MAX_JSON = 4L * 1024 * 1024
  }
}

/** checkUpdate of phone.md P16.3: the hub first, then GitHub directly. */
class UpdateClient(
  private val net: Fetcher,
  private val repo: String = DEFAULT_REPO,
  private val githubApi: String = "https://api.github.com",
) {
  /**
   * The offered update, or null when none is newer (or this phone's SDK is too old for it).
   * The hub is asked first with the device token; GitHub is used only when the hub has no
   * release or cannot be reached. Throws when neither can be reached.
   */
  fun check(origin: String?, token: String?, installedBuild: Long, sdk: Int): UpdateOffer? {
    var reached = false
    if (origin != null && token != null) {
      val r = net.get(origin.trimEnd('/') + "/v1/app/android", token)
      if (r.body != null) reached = true
      if (r.status == 200 && r.body != null) {
        val hub = Updates.hubOffer(origin, r.body)
        if (hub != null) return if (Updates.isOffered(hub.first, installedBuild, sdk)) UpdateOffer(hub.first, hub.second, "hub") else null
      }
      // 404 (nothing mirrored), a mirror turned off, an error or no answer: GitHub next.
    }
    val rs = net.get("$githubApi/repos/$repo/releases?per_page=20", null)
    if (rs.body != null) reached = true
    val rel = if (rs.status == 200 && rs.body != null) Updates.newestRelease(rs.body) else null
    if (rel != null) {
      val mu = rel.assets["manifest.json"]
      val mr = if (mu != null) net.get(mu, null) else null
      val m = if (mr != null && mr.status == 200 && mr.body != null) Updates.parseManifest(mr.body) else null
      if (m != null && "phone-v${m.version}" == rel.tag) {
        val apk = rel.assets[m.apk]
        if (apk != null && Updates.isOffered(m, installedBuild, sdk)) return UpdateOffer(m, apk, "github")
        return null
      }
    }
    if (!reached) throw UpdateException("can't reach the hub or GitHub")
    return null
  }

  companion object {
    /** The build's repository (version.Repo on the hub side); a fork changes both. */
    const val DEFAULT_REPO = "mraj602/switchyard"
  }
}
