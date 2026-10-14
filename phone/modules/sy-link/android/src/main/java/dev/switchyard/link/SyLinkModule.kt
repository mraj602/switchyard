package dev.switchyard.link

import android.annotation.SuppressLint
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import expo.modules.kotlin.Promise
import expo.modules.kotlin.exception.CodedException
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import org.json.JSONObject
import java.security.SecureRandom

/**
 * The Expo module (push.md 14.8). JavaScript only configures the link and reads its state;
 * everything that must run without React Native lives in the service, the receivers and the
 * watchdog.
 */
class SyLinkModule : Module() {
  private val ctx: Context
    get() = appContext.reactContext?.applicationContext ?: throw CodedException("ERR_SY_CONTEXT", "sy-link: no Android context", null)

  private val store get() = Link.store(ctx)
  private var batteryPromise: Promise? = null

  override fun definition() = ModuleDefinition {
    Name("SyLink")
    Events("state", "refresh", "revoked", "upgrade")

    OnCreate { current = this@SyLinkModule }
    OnDestroy { if (current === this@SyLinkModule) current = null }

    AsyncFunction("prepareKeys") {
      val s = store
      if (!s.keysFresh || !Keystore.has() || s.auth == null) {
        Keystore.create()
        s.saveKeys(ByteArray(16).also { SecureRandom().nextBytes(it) })
      }
      val sign = Keystore.signer().publicKey
      val box = Keystore.agreement().publicKey
      mapOf(
        "sign" to mapOf("alg" to "p256", "pub" to Codec.b64u(sign)),
        "box" to mapOf("alg" to "p256", "pub" to Codec.b64u(box)),
        "auth" to Codec.b64u(s.auth!!),
      )
    }

    AsyncFunction("sealKeys") { k: String, secret: String ->
      val key = Codec.unb64u(k) ?: throw CodedException("ERR_SY_SEAL", "sy-link: k is not base64url", null)
      val auth = store.auth ?: throw CodedException("ERR_SY_KEYS", "sy-link: prepareKeys first", null)
      val json = Pairing.keysJson(Keystore.signer().publicKey, Keystore.agreement().publicKey, auth)
      try {
        ClaimSeal.seal(key, secret, Codec.utf8(json.toString()), ByteArray(12).also { SecureRandom().nextBytes(it) })
      } catch (e: CryptoException) {
        throw CodedException("ERR_SY_SEAL", "sy-link: ${e.message}", null)
      }
    }

    AsyncFunction("configure") { cfg: Map<String, Any?> ->
      val origin = cfg["origin"] as? String ?: throw bad("origin")
      val device = cfg["device"] as? String ?: throw bad("device")
      @Suppress("UNCHECKED_CAST")
      val urls = (cfg["urls"] as? List<Any?>)?.filterIsInstance<String>()?.filter { it.startsWith("wss://") || it.startsWith("ws://") }
        ?: throw bad("urls")
      if (urls.isEmpty()) throw bad("urls")
      val pingS = (cfg["pingS"] as? Number)?.toLong()?.coerceIn(60, 900) ?: Store.DEFAULT_PING_S
      @Suppress("UNCHECKED_CAST")
      val hubKey = cfg["hubKey"] as? Map<String, Any?> ?: throw bad("hubKey")
      val pub = Codec.unb64u(hubKey["pub"] as? String) ?: throw bad("hubKey.pub")
      val fp = hubKey["fp"] as? String ?: throw bad("hubKey.fp")
      val reg = cfg["reg"] as? String ?: throw bad("reg")
      @Suppress("UNCHECKED_CAST")
      val cert = (cfg["cert"] as? Map<String, Any?>)?.let { JSONObject(it) }
      val qrFp = cfg["qrFp"] as? String
      try {
        Pairing.check(pub, fp, qrFp, device, reg, cert, Keystore.signer().publicKey, Keystore.agreement().publicKey)
      } catch (e: Pairing.Refused) {
        throw CodedException(if (e.code == "hub_key_mismatch") "ERR_SY_HUB_KEY" else "ERR_SY_TAMPERED", e.message, null)
      }
      store.configure(origin, device, urls, pingS, pub, fp, reg)
      store.setKeysFresh(false)
      Unit
    }

    AsyncFunction("start") {
      val s = store
      if (!s.configured) throw CodedException("ERR_SY_NOT_CONFIGURED", "sy-link: configure first", null)
      s.setEnabled(true)
      Watchdog.schedule(ctx)
      if (SyLinkService.instance == null) SyLinkService.start(ctx) else SyLinkService.start(ctx, SyLinkService.ACTION_RECONNECT)
      Unit
    }

    AsyncFunction("stop") {
      store.setEnabled(false)
      SyLinkService.stop(ctx)
      store.setConnection("off", null, null, null, null)
      if (store.configured) Watchdog.schedule(ctx) // 15-minute checks (14.9)
      emit("state", Link.state(ctx, store))
      Unit
    }

    AsyncFunction("reconnect") {
      if (store.enabled && store.configured) SyLinkService.start(ctx, SyLinkService.ACTION_RECONNECT)
      Unit
    }

    AsyncFunction("getState") { Link.state(ctx, store) }

    AsyncFunction("setPingInterval") { seconds: Int ->
      if (seconds < 60 || seconds > 900) throw CodedException("ERR_SY_RANGE", "sy-link: ping interval must be 60–900 s", null)
      store.setPing(seconds.toLong())
      if (store.enabled && SyLinkService.instance != null) SyLinkService.start(ctx, SyLinkService.ACTION_RECONNECT)
      Unit
    }

    AsyncFunction("setSnoozeMinutes") { minutes: Int ->
      if (minutes in 1..1440) store.setSnooze(minutes.toLong())
      Unit
    }

    AsyncFunction("requestBatteryExemption") { promise: Promise ->
      val activity = appContext.currentActivity
      if (Link.batteryExempt(ctx) || activity == null) return@AsyncFunction promise.resolve(Link.batteryExempt(ctx))
      batteryPromise?.resolve(Link.batteryExempt(ctx))
      batteryPromise = promise
      try {
        @SuppressLint("BatteryLife")
        val i = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:${ctx.packageName}"))
        activity.startActivityForResult(i, BATTERY_REQUEST)
      } catch (e: Exception) {
        batteryPromise = null
        promise.resolve(Link.batteryExempt(ctx))
      }
    }

    OnActivityResult { _, payload ->
      if (payload.requestCode != BATTERY_REQUEST) return@OnActivityResult
      batteryPromise?.resolve(Link.batteryExempt(ctx))
      batteryPromise = null
    }

    AsyncFunction("openBatterySettings") {
      openBatterySettings(ctx)
      Unit
    }

    AsyncFunction("sendLocalTest") {
      Notifier(ctx, store).localTest()
      Unit
    }

    AsyncFunction("getRefresh") { store.refresh }

    AsyncFunction("reset") {
      SyLinkService.stop(ctx)
      Watchdog.cancel(ctx)
      Keystore.delete()
      store.clear()
      Unit
    }

    // The updater (phone.md P16) comes in N2; the API shape is here so the app compiles against it.
    AsyncFunction("checkUpdate") { _: String, _: String -> notYet() }
    AsyncFunction("installUpdate") { _: Map<String, Any?> -> notYet() }
  }

  private fun notYet(): String? = throw CodedException("ERR_SY_UPDATE", "sy-link: the updater is not built yet (N2)", null)

  private fun bad(field: String) = CodedException("ERR_SY_CONFIG", "sy-link: configure needs $field", null)

  companion object {
    @Volatile private var current: SyLinkModule? = null
    private const val BATTERY_REQUEST = 0x5359

    /** Events to JS while the app runs; dropped otherwise (the state is in Store either way). */
    fun emit(name: String, body: Map<String, Any?>?) {
      try {
        current?.sendEvent(name, body ?: emptyMap())
      } catch (_: Exception) {
        // the React context is gone
      }
    }

    /** phone.md P6.7: the maker's own screen when known, else Android's battery list, else the app's page. */
    fun openBatterySettings(ctx: Context) {
      val maker = Build.MANUFACTURER.lowercase()
      val known = when {
        maker.contains("samsung") -> listOf("com.samsung.android.lool" to "com.samsung.android.sm.battery.ui.BatteryActivity")
        maker.contains("xiaomi") || maker.contains("redmi") || maker.contains("poco") ->
          listOf("com.miui.securitycenter" to "com.miui.permcenter.autostart.AutoStartManagementActivity")
        maker.contains("oneplus") || maker.contains("oppo") || maker.contains("realme") -> listOf(
          "com.coloros.safecenter" to "com.coloros.safecenter.permission.startup.StartupAppListActivity",
          "com.oplus.battery" to "com.oplus.powermanager.fuelgaue.PowerUsageModelActivity",
        )
        maker.contains("huawei") || maker.contains("honor") ->
          listOf("com.huawei.systemmanager" to "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity")
        else -> emptyList()
      }
      val intents = known.map { (p, c) -> Intent().setComponent(ComponentName(p, c)) } + listOf(
        Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS),
        Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:${ctx.packageName}")),
      )
      for (i in intents) {
        try {
          ctx.startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
          return
        } catch (_: Exception) {
          // not on this phone: the next one
        }
      }
    }
  }
}
