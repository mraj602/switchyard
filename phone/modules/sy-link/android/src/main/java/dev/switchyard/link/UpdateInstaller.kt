package dev.switchyard.link

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import java.io.File

/** The PackageManager's facts for [CertCheck]: the archive's and the installed app's signers. */
class AndroidSigningSource(private val ctx: Context) : SigningSource {
  private val pm get() = ctx.packageManager

  override fun archive(path: String): ArchiveFacts? = try {
    @Suppress("DEPRECATION")
    val p = pm.getPackageArchiveInfo(path, PackageManager.GET_SIGNING_CERTIFICATES)
    p?.let { ArchiveFacts(it.packageName, it.longVersionCode, signers(it)) }
  } catch (_: Exception) {
    null
  }

  override fun installed(): ArchiveFacts? = try {
    @Suppress("DEPRECATION")
    val p = pm.getPackageInfo(ctx.packageName, PackageManager.GET_SIGNING_CERTIFICATES)
    ArchiveFacts(p.packageName, p.longVersionCode, signers(p))
  } catch (_: Exception) {
    null
  }

  /** apkContentsSigners: the certificates the APK's contents are signed with (not the rotation history). */
  private fun signers(p: android.content.pm.PackageInfo): List<ByteArray> =
    p.signingInfo?.apkContentsSigners?.map { it.toByteArray() } ?: emptyList()
}

/**
 * installUpdate of phone.md P16.3: download to the app's cache, check the SHA-256 against the
 * manifest, check the signing certificate against the manifest and the installed app, then
 * hand the file to PackageInstaller. Android asks the user to confirm.
 */
class UpdateInstaller(
  private val ctx: Context,
  private val net: Fetcher = OkFetcher(),
  private val source: SigningSource = AndroidSigningSource(ctx),
) {
  sealed class Result {
    object Started : Result()
    class Refused(val why: String) : Result()
  }

  private val dir get() = File(ctx.cacheDir, "updates")

  /** [token] is the hub's bearer token for a hub download, [origin] the hub it belongs to. */
  fun install(offer: UpdateOffer, origin: String?, token: String?): Result {
    val m = offer.manifest
    if (!offer.url.startsWith("https://") && !(offer.source == "hub" && origin != null && offer.url.startsWith(origin))) {
      return Result.Refused("the update address is not https")
    }
    clean()
    dir.mkdirs()
    val file = File(dir, "update.apk")
    val bearer = if (Updates.sendsToken(offer.url, origin)) token else null
    val got = net.download(offer.url, bearer, file, MAX_APK) ?: return refuse(file, "the download failed")
    if (got != m.sha256.lowercase()) return refuse(file, "This update does not match its checksum.")
    val verdict = CertCheck.verify(source, file.absolutePath, m.certSha256)
    if (verdict != Verdict.OK) return refuse(file, message(verdict))
    return try {
      commit(file)
      Result.Started
    } catch (e: Exception) {
      refuse(file, "the installer refused: ${e.javaClass.simpleName}")
    }
  }

  private fun commit(file: File) {
    val pi = ctx.packageManager.packageInstaller
    val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
    params.setAppPackageName(ctx.packageName)
    val id = pi.createSession(params)
    pi.openSession(id).use { s ->
      file.inputStream().use { input -> s.openWrite("sy-update", 0, file.length()).use { out -> input.copyTo(out); s.fsync(out) } }
      val intent = Intent(ctx, InstallResultReceiver::class.java).setPackage(ctx.packageName)
      // Mutable: the system adds the status and the confirmation Intent as extras.
      val pending = PendingIntent.getBroadcast(ctx, id, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE)
      s.commit(pending.intentSender)
    }
  }

  private fun refuse(f: File, why: String): Result {
    f.delete()
    return Result.Refused(why)
  }

  fun clean() {
    dir.listFiles()?.forEach { it.delete() }
  }

  companion object {
    const val MAX_APK = 300L * 1024 * 1024

    fun message(v: Verdict) = when (v) {
      Verdict.OK -> ""
      Verdict.NOT_AN_APK -> "The downloaded file is not an app."
      Verdict.WRONG_PACKAGE -> "This update is for a different app."
      Verdict.NOT_NEWER -> "This update is not newer than the installed app."
      Verdict.UNSIGNED, Verdict.MANIFEST_CERT_MISMATCH, Verdict.INSTALLED_CERT_MISMATCH -> "This update is not signed by your key."
    }
  }
}

/** The installer's answer (a manifest receiver, not exported): opens the confirmation, cleans up. */
class InstallResultReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
      PackageInstaller.STATUS_PENDING_USER_ACTION -> {
        @Suppress("DEPRECATION")
        val confirm = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT)
        if (confirm != null) {
          try {
            ctx.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
          } catch (_: Exception) {
            // the user will find the installer's notification instead
          }
        }
      }
      else -> UpdateInstaller(ctx).clean() // success (the process is replaced anyway), abort or failure
    }
  }
}
