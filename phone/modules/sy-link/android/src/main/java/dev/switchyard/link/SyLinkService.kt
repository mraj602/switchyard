package dev.switchyard.link

import android.app.AlarmManager
import android.app.ForegroundServiceStartNotAllowedException
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.net.ConnectivityManager
import android.net.Network
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.util.Log
import androidx.core.app.NotificationManagerCompat
import org.json.JSONObject

/** The module's own SharedPreferences (push.md 14.4 "Storage"; credential-protected storage). */
class PrefsKV(ctx: Context) : KV {
  private val p: SharedPreferences = ctx.applicationContext.getSharedPreferences("sy_link", Context.MODE_PRIVATE)

  override fun getString(k: String): String? = p.getString(k, null)
  override fun getLong(k: String, def: Long): Long = p.getLong(k, def)
  override fun getBoolean(k: String, def: Boolean): Boolean = p.getBoolean(k, def)
  override fun put(values: Map<String, Any?>, commit: Boolean) {
    val e = p.edit()
    for ((k, v) in values) when (v) {
      null -> e.remove(k)
      is String -> e.putString(k, v)
      is Long -> e.putLong(k, v)
      is Int -> e.putLong(k, v.toLong())
      is Boolean -> e.putBoolean(k, v)
      else -> e.putString(k, v.toString())
    }
    if (commit) e.commit() else e.apply()
  }
  override fun clear() {
    p.edit().clear().commit()
  }
}

/** Shared construction for the service and the watchdog. */
object Link {
  @Volatile private var store: Store? = null

  fun store(ctx: Context): Store = store ?: synchronized(this) { store ?: Store(PrefsKV(ctx)).also { store = it } }

  fun clientInfo(ctx: Context): JSONObject {
    val pi = try {
      ctx.packageManager.getPackageInfo(ctx.packageName, 0)
    } catch (_: Exception) {
      null
    }
    return JSONObject().put("kind", "android").put("app", pi?.versionName ?: "0").put("build", (pi?.longVersionCode ?: 0).toString())
      .put("sdk", Build.VERSION.SDK_INT)
  }

  /** PARTIAL_WAKE_LOCK only around one frame, 10 s cap (push.md 14.4). */
  fun waker(ctx: Context): (() -> Unit) -> Unit {
    val pm = ctx.getSystemService(PowerManager::class.java)
    return { f ->
      val wl = pm?.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "sy:link-frame")
      wl?.setReferenceCounted(false)
      wl?.acquire(10_000)
      try {
        f()
      } finally {
        if (wl?.isHeld == true) wl.release()
      }
    }
  }

  fun client(ctx: Context, store: Store, notifier: Notifier, events: LinkEvents): LinkClient {
    val agreement = Keystore.agreement()
    val processor = FrameProcessor(
      store,
      verifier = { sig, data -> store.hubPub?.let { TinkHubVerifier(it).verify(sig, data) } ?: false },
      open = { body -> Rfc8291.decrypt(body, agreement, store.auth ?: throw CryptoException("no_auth", "no auth secret")) },
      sink = notifier,
      nowMs = System::currentTimeMillis,
    )
    return LinkClient(store, Keystore.signer(), processor, events, { clientInfo(ctx) }, waker(ctx))
  }

  fun batteryExempt(ctx: Context): Boolean =
    ctx.getSystemService(PowerManager::class.java)?.isIgnoringBatteryOptimizations(ctx.packageName) == true

  fun canExact(ctx: Context): Boolean = ctx.getSystemService(AlarmManager::class.java)?.canScheduleExactAlarms() == true

  /** An alarm to AlarmReceiver: exact when allowed (14.2 "Exact alarms"), else inexact while idle. */
  fun alarm(ctx: Context, action: String, atMs: Long, exactOnlyWhenExempt: Boolean) {
    val am = ctx.getSystemService(AlarmManager::class.java) ?: return
    val pi = PendingIntent.getBroadcast(
      ctx, action.hashCode(), Intent(ctx, AlarmReceiver::class.java).setAction(action),
      PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
    val exact = canExact(ctx) && (!exactOnlyWhenExempt || batteryExempt(ctx))
    try {
      if (exact) am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, atMs, pi)
      else am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, atMs, pi)
    } catch (e: SecurityException) {
      am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, atMs, pi)
    }
  }

  /** LinkState of push.md 14.8, as the JS API returns it. */
  fun state(ctx: Context, store: Store): Map<String, Any?> = mapOf(
    "enabled" to store.enabled,
    "status" to (if (!store.configured) "off" else if (!store.enabled && store.status !in setOf("revoked", "upgrade", "error")) "off" else store.status),
    "transport" to store.transport,
    "connectedSince" to store.connectedSince?.toDouble(),
    "nextRetryAt" to store.nextRetryAt?.toDouble(),
    "lastError" to store.lastError,
    "lastSeq" to store.last.toDouble(),
    "received" to store.received.toDouble(),
    "rejected" to store.rejected.toDouble(),
    "batteryExempt" to batteryExempt(ctx),
    "notificationsAllowed" to NotificationManagerCompat.from(ctx).areNotificationsEnabled(),
    "verified" to store.verified,
    "pingS" to store.pingS.toDouble(),
    "checkCode" to checkCode(),
  )

  /** push.md 14.7: the first 8 characters of the sign key's fingerprint, grouped "k7q2-m4xa". */
  private fun checkCode(): String? = try {
    if (!Keystore.has()) null else Codec.fingerprint(Keystore.signer().publicKey).take(8).chunked(4).joinToString("-")
  } catch (_: Exception) {
    null
  }
}

/**
 * The foreground service (push.md 14.4): one link connection while instant delivery is on, the
 * persistent notification on `sy_connection`, a reconnect on every new default network, and a
 * restart alarm when the task is removed.
 */
class SyLinkService : Service(), LinkEvents {
  private lateinit var store: Store
  private lateinit var notifier: Notifier
  private var client: LinkClient? = null
  private var netCb: ConnectivityManager.NetworkCallback? = null

  override fun onBind(intent: Intent?): IBinder? = null

  override fun onCreate() {
    super.onCreate()
    store = Link.store(this)
    notifier = Notifier(this, store)
    notifier.ensureChannels()
    instance = this
  }

  /** 14.2: within 5 s of startForegroundService, per API level. */
  private fun goForeground() {
    Foreground.go(
      Build.VERSION.SDK_INT, notifier.connection(),
      plain = { n -> startForeground(NotifyIds.CONNECTION, n) },
      typed = { n, type -> startForeground(NotifyIds.CONNECTION, n, type) },
    )
  }

  override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
    goForeground()
    when (intent?.action) {
      ACTION_STOP -> {
        client?.stop()
        stopSelfNow()
        return START_NOT_STICKY
      }
      ACTION_RECONNECT -> client?.reconnectNow()
      ACTION_RETRY -> client?.retryIfWaiting()
    }
    if (!store.enabled || !store.configured || store.verified == "changed") {
      stopSelfNow()
      return START_NOT_STICKY
    }
    if (client == null) {
      client = try {
        Link.client(this, store, notifier, this)
      } catch (e: Exception) {
        Log.w(LinkClient.TAG, "no keys: ${e.javaClass.simpleName}")
        store.setConnection("error", null, null, null, "no_keys")
        stopSelfNow()
        return START_NOT_STICKY
      }
      watchNetwork()
    }
    client?.start()
    return START_STICKY
  }

  private fun watchNetwork() {
    val cm = getSystemService(ConnectivityManager::class.java) ?: return
    val cb = object : ConnectivityManager.NetworkCallback() {
      private var current: Network? = null

      override fun onAvailable(network: Network) {
        // The first call reports the network we already have; a different one is a switch
        // (Wi-Fi ↔ mobile, a VPN such as Tailscale coming up): reconnect at once, backoff reset.
        val before = current
        current = network
        if (before != null && before != network) client?.reconnectNow()
      }
    }
    try {
      cm.registerDefaultNetworkCallback(cb)
      netCb = cb
    } catch (e: Exception) {
      Log.w(LinkClient.TAG, "network callback: ${e.javaClass.simpleName}")
    }
  }

  override fun onTaskRemoved(rootIntent: Intent?) {
    super.onTaskRemoved(rootIntent)
    // An alarm firing is one of the allowed background starts (14.2).
    if (store.enabled) Link.alarm(this, AlarmReceiver.RESTART, System.currentTimeMillis() + 1000, exactOnlyWhenExempt = false)
  }

  override fun onDestroy() {
    netCb?.let { cb -> getSystemService(ConnectivityManager::class.java)?.unregisterNetworkCallback(cb) }
    netCb = null
    client?.shutdown()
    client = null
    if (instance === this) instance = null
    super.onDestroy()
  }

  private fun stopSelfNow() {
    stopForeground(STOP_FOREGROUND_REMOVE)
    stopSelf()
  }

  // ------------------------------------------------------------------ LinkEvents

  override fun state() {
    try {
      getSystemService(NotificationManager::class.java)?.notify(NotifyIds.CONNECTION, notifier.connection())
    } catch (_: SecurityException) {
      // notifications blocked: the service still runs
    }
    SyLinkModule.emit("state", Link.state(this, store))
  }

  override fun stopped(status: String, why: String) {
    onStopped(this, store, notifier, status, why)
    stopSelfNow()
  }

  override fun longTimer(atMs: Long) = Link.alarm(this, AlarmReceiver.RETRY, atMs, exactOnlyWhenExempt = true)

  /** An action tapped while the service runs: the link first (it is up), in the client's time. */
  fun runAction(a: PendingAction) {
    val c = client
    Thread({
      val transport = c ?: object : ActionTransport {
        override fun viaLink(id: String, token: String, timeoutMs: Long): Int? = null
        override fun viaHttp(origin: String, token: String, timeoutMs: Long): Int? = LinkClient.postAction(origin, token, timeoutMs)
      }
      ActionRunner(store, transport, System::currentTimeMillis).run(a)
    }, "sy-link-action").start()
  }

  companion object {
    const val ACTION_STOP = "dev.switchyard.link.STOP"
    const val ACTION_RECONNECT = "dev.switchyard.link.RECONNECT"
    const val ACTION_RETRY = "dev.switchyard.link.RETRY"

    @Volatile var instance: SyLinkService? = null

    /**
     * startForegroundService, which Android 12+ refuses from the background unless an exemption
     * applies (14.2). Refused → false, and Settings says delivery is paused.
     */
    fun start(ctx: Context, action: String? = null): Boolean = try {
      ctx.startForegroundService(Intent(ctx, SyLinkService::class.java).setAction(action))
      true
    } catch (e: ForegroundServiceStartNotAllowedException) {
      Link.store(ctx).setError(BACKGROUND_BLOCKED)
      SyLinkModule.emit("state", Link.state(ctx, Link.store(ctx)))
      false
    } catch (e: IllegalStateException) {
      Link.store(ctx).setError(BACKGROUND_BLOCKED)
      false
    }

    fun stop(ctx: Context) {
      instance?.let {
        it.client?.stop()
        it.stopSelfNow()
      }
    }

    /** What 4403, 4426 and the other stops do, for the service and the watchdog alike. */
    fun onStopped(ctx: Context, store: Store, notifier: Notifier, status: String, why: String) {
      store.setConnection(status, null, null, null, why)
      when (status) {
        "revoked" -> {
          store.setEnabled(false)
          Watchdog.cancel(ctx)
          notifier.stopped("This phone was removed from the hub")
          SyLinkModule.emit("revoked", null)
        }
        "upgrade" -> {
          notifier.stopped("Update the app to keep getting notifications")
          SyLinkModule.emit("upgrade", null)
        }
        else -> notifier.stopped(why)
      }
      SyLinkModule.emit("state", Link.state(ctx, store))
    }

    /** LinkState.lastError when Android refused a background start (Settings → Notifications explains it). */
    const val BACKGROUND_BLOCKED = "background_start_blocked"
  }
}
