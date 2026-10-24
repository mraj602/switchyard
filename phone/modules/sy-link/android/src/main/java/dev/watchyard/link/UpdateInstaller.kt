package dev.watchyard.link

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
 * hand the file to PackageInstaller. Android asks the user to confirm. One install at a time,
 * and the bytes written into the session are hashed again ([Updates.copyChecked]), so the file
 * cannot change between the checks and the install.
 */
class UpdateInstaller(
  private val dir: File,
  private val net: Fetcher,
  private val source: SigningSource,
  /** Writes the file into an installer session and commits it, checking its SHA-256 on the way; throws to refuse. */
  private val writeSession: (File, String) -> Unit,
) {
  constructor(ctx: Context) : this(File(ctx.cacheDir, "updates"), OkFetcher(), AndroidSigningSource(ctx), { f, sha -> session(ctx, f, sha) })

  sealed class Result {
    object Started : Result()
    class Refused(val why: String) : Result()
  }

  /** [token] is the hub's bearer token for a hub download, [origin] the hub it belongs to. */
  fun install(offer: UpdateOffer, origin: String?, token: String?): Result = synchronized(LOCK) {
    val m = offer.manifest
    val fromHub = offer.source == "hub" && Updates.sendsToken(offer.url, origin)
    if (!offer.url.startsWith("https://") && !fromHub) return Result.Refused("the update address is not https")
    dir.listFiles()?.forEach { it.delete() } // what a killed process left
    dir.mkdirs()
    val file = File(dir, "update.apk")
    try {
      val got = net.download(offer.url, if (fromHub) token else null, file, MAX_APK) ?: return Result.Refused("the download failed")
      if (got != m.sha256.lowercase()) return Result.Refused("This update does not match its checksum.")
      val verdict = CertCheck.verify(source, file.absolutePath, m.certSha256)
      if (verdict != Verdict.OK) return Result.Refused(message(verdict))
      try {
        writeSession(file, m.sha256.lowercase())
        Result.Started
      } catch (e: Exception) {
        Result.Refused("the installer refused: ${e.javaClass.simpleName}")
      }
    } finally {
      file.delete() // the session holds its own copy
    }
  }

  companion object {
    const val MAX_APK = 300L * 1024 * 1024
    private val LOCK = Any()

    private fun session(ctx: Context, file: File, sha256: String) {
      val pi = ctx.packageManager.packageInstaller
      val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
      params.setAppPackageName(ctx.packageName)
      val id = pi.createSession(params)
      pi.openSession(id).use { s ->
        try {
          file.inputStream().use { input -> s.openWrite("sy-update", 0, file.length()).use { out -> Updates.copyChecked(input, out, sha256); s.fsync(out) } }
        } catch (e: Exception) {
          s.abandon()
          throw e
        }
        val intent = Intent(ctx, InstallResultReceiver::class.java).setPackage(ctx.packageName)
        // Mutable: the system adds the status and the confirmation Intent as extras.
        val pending = PendingIntent.getBroadcast(ctx, id, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE)
        s.commit(pending.intentSender)
      }
    }

    fun message(v: Verdict) = when (v) {
      Verdict.OK -> ""
      Verdict.NOT_AN_APK -> "The downloaded file is not an app."
      Verdict.WRONG_PACKAGE -> "This update is for a different app."
      Verdict.NOT_NEWER -> "This update is not newer than the installed app."
      Verdict.UNSIGNED, Verdict.MANIFEST_CERT_MISMATCH, Verdict.INSTALLED_CERT_MISMATCH -> "This update is not signed by your key."
    }
  }
}

/** The installer's answer (a manifest receiver, not exported): opens Android's confirmation. */
class InstallResultReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    if (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE) != PackageInstaller.STATUS_PENDING_USER_ACTION) return
    @Suppress("DEPRECATION")
    val confirm = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT) ?: return
    try {
      ctx.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    } catch (_: Exception) {
      // the user will find the installer's notification instead
    }
  }
}
