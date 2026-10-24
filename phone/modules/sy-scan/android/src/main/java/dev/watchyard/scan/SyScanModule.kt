package dev.watchyard.scan

import android.Manifest
import com.journeyapps.barcodescanner.ScanIntentResult
import com.journeyapps.barcodescanner.ScanOptions
import expo.modules.interfaces.permissions.Permissions
import expo.modules.interfaces.permissions.PermissionsResponse
import expo.modules.interfaces.permissions.PermissionsStatus
import expo.modules.kotlin.Promise
import expo.modules.kotlin.exception.CodedException
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

/**
 * The pairing QR scanner (phone.md P2): asks for the camera, opens zxing's scanner screen for QR
 * codes only and resolves with the text of the first code it reads. Nothing is kept: the text
 * goes to the Pair screen, which parses it (parsePairInput).
 */
class SyScanModule : Module() {
  private var pending: Promise? = null

  private val permissions: Permissions
    get() = appContext.permissions ?: throw CodedException("ERR_SY_PERMISSIONS", "sy-scan: no permissions manager", null)

  override fun definition() = ModuleDefinition {
    Name("SyScan")

    /** {granted, canAskAgain} without asking. */
    AsyncFunction("cameraPermission") { promise: Promise ->
      permissions.getPermissions({ promise.resolve(answer(it)) }, Manifest.permission.CAMERA)
    }

    /**
     * Asks for the camera if needed, then scans. Resolves {status: "scanned", text},
     * {status: "cancelled"} (Back) or {status: "denied", canAskAgain}.
     */
    AsyncFunction("scan") { prompt: String, promise: Promise ->
      if (pending != null) {
        promise.reject(CodedException("ERR_SY_SCAN_BUSY", "sy-scan: a scan is already open", null))
        return@AsyncFunction
      }
      permissions.askForPermissions({ result ->
        val a = answer(result)
        if (a["granted"] != true) {
          promise.resolve(mapOf("status" to "denied", "canAskAgain" to a["canAskAgain"]))
          return@askForPermissions
        }
        val activity = appContext.currentActivity
        if (activity == null) {
          promise.reject(CodedException("ERR_SY_SCAN_ACTIVITY", "sy-scan: no activity", null))
          return@askForPermissions
        }
        val options = ScanOptions()
          .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
          .setPrompt(prompt)
          .setBeepEnabled(false)
          .setOrientationLocked(true)
          .setCaptureActivity(SyScanActivity::class.java)
        pending = promise
        try {
          activity.startActivityForResult(options.createScanIntent(activity), REQUEST_CODE)
        } catch (e: Exception) {
          pending = null
          promise.reject(CodedException("ERR_SY_SCAN_START", "sy-scan: ${e.javaClass.simpleName}", null))
        }
      }, Manifest.permission.CAMERA)
    }

    OnActivityResult { _, payload ->
      if (payload.requestCode != REQUEST_CODE) return@OnActivityResult
      val p = pending ?: return@OnActivityResult
      pending = null
      val text = ScanIntentResult.parseActivityResult(payload.resultCode, payload.data).contents
      p.resolve(if (text != null) mapOf("status" to "scanned", "text" to text) else mapOf("status" to "cancelled"))
    }
  }

  private fun answer(map: Map<String, PermissionsResponse>): Map<String, Boolean> {
    val r = map[Manifest.permission.CAMERA]
    return mapOf("granted" to (r?.status == PermissionsStatus.GRANTED), "canAskAgain" to (r?.canAskAgain ?: true))
  }

  companion object {
    /** Any value unlikely to collide with another module's request code. */
    const val REQUEST_CODE = 0x5359
  }
}
