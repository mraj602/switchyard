package dev.switchyard.link

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat

/**
 * push.md 14.5 and 14.6 on the platform: P6.1's channels, posting with the tag-derived id
 * (a new alert with the same tag replaces the old one), dismissals, Open / Snooze / Seen, the
 * persistent connection notification, and the widget redraw of a `refresh` frame.
 */
class Notifier(private val ctx: Context, private val store: Store) : Sink {
  private val nm get() = NotificationManagerCompat.from(ctx)

  /** Called by the service and before any post; idempotent (the app's JS makes the same ids). */
  fun ensureChannels() {
    val m = ctx.getSystemService(NotificationManager::class.java) ?: return
    for ((id, name, importance) in Channels.ALL) {
      if (m.getNotificationChannel(id) != null) continue
      val ch = NotificationChannel(id, name, importance)
      if (id == Channels.CONNECTION) {
        ch.setShowBadge(false)
        ch.setSound(null, null)
        ch.enableVibration(false)
      }
      m.createNotificationChannel(ch)
    }
  }

  private fun icon(): Int {
    val r = ctx.resources.getIdentifier("notification_icon", "drawable", ctx.packageName)
    return if (r != 0) r else R.drawable.sy_link_notify
  }

  private fun flags() = PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT

  /** Tap and Open: the app at the payload's route, or the Claude link when the tap rule says so. */
  private fun openIntent(n: AlertNote): PendingIntent {
    val i = if (n.tap == "claude" && n.remoteUrl != null) {
      Intent(Intent.ACTION_VIEW, Uri.parse(n.remoteUrl))
    } else {
      Intent(Intent.ACTION_VIEW, Uri.parse(Payloads.appLink(n.url))).setPackage(ctx.packageName)
    }
    i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    return PendingIntent.getActivity(ctx, n.id, i, flags())
  }

  private fun actionIntent(n: AlertNote, action: String, token: String): PendingIntent {
    val i = Intent(ctx, ActionReceiver::class.java)
      .setAction("dev.switchyard.link.ACTION.$action")
      .putExtra(ActionReceiver.EXTRA_ACTION, action)
      .putExtra(ActionReceiver.EXTRA_TOKEN, token)
      .putExtra(ActionReceiver.EXTRA_ALERT, n.alert ?: "")
      .putExtra(ActionReceiver.EXTRA_TAG, n.tag)
      .putExtra(ActionReceiver.EXTRA_ORIGIN, n.origin ?: store.origin ?: "")
    return PendingIntent.getBroadcast(ctx, n.id * 31 + action.hashCode(), i, flags())
  }

  override fun post(n: AlertNote) {
    // An action meant for another hub is dropped (phone.md P6.4): its buttons are not offered.
    val sameHub = n.origin == null || store.origin == null || sameOrigin(n.origin, store.origin!!)
    val b = NotificationCompat.Builder(ctx, n.channel)
      .setSmallIcon(icon())
      .setContentTitle(n.title)
      .setContentText(n.body)
      .setStyle(NotificationCompat.BigTextStyle().bigText(n.body))
      .setGroup(Channels.GROUP)
      .setAutoCancel(true)
      .setContentIntent(openIntent(n))
      .setCategory(NotificationCompat.CATEGORY_MESSAGE)
    n.whenMs?.let { b.setWhen(it).setShowWhen(true) }
    if (sameHub && (n.snooze != null || n.seen != null)) {
      b.addAction(0, "Open", openIntent(n))
      n.snooze?.let { b.addAction(0, "Snooze ${store.snoozeMin} min", actionIntent(n, "snooze", it)) }
      n.seen?.let { b.addAction(0, "Seen", actionIntent(n, "seen", it)) }
    }
    notify(n.id, b.build())
  }

  override fun dismiss(tag: String) = nm.cancel(NotifyIds.forTag(tag))

  override fun missed() = SyLinkModule.emit("refresh", null)

  /**
   * A `refresh` frame (push.md 13.5): keep the snapshot and ask the home-screen widgets to
   * redraw. react-native-android-widget's providers start their headless JS task on
   * APPWIDGET_UPDATE; that task merges this snapshot (SyLink.getRefresh) into the last one
   * (phone.md P11) with no network call. A running app also gets the `refresh` event.
   */
  override fun refresh(json: String) {
    store.setRefresh(json)
    SyLinkModule.emit("refresh", null)
    try {
      val awm = AppWidgetManager.getInstance(ctx)
      for (p in awm.getInstalledProvidersForPackage(ctx.packageName, null)) {
        val ids = awm.getAppWidgetIds(p.provider)
        if (ids.isEmpty()) continue
        ctx.sendBroadcast(
          Intent(AppWidgetManager.ACTION_APPWIDGET_UPDATE).setComponent(ComponentName(ctx, p.provider.className))
            .putExtra(AppWidgetManager.EXTRA_APPWIDGET_IDS, ids),
        )
      }
    } catch (e: Exception) {
      Log.w(LinkClient.TAG, "widget redraw: ${e.javaClass.simpleName}")
    }
  }

  /** The persistent notification of 14.4 (channel sy_connection). Tapping it opens Settings → Notifications. */
  fun connection(): Notification {
    val text = when (store.status) {
      "connected" -> "Connected to hub" + (store.connectedSince?.let { " · ${ago(it)}" } ?: "")
      "backoff" -> "Reconnecting…" + (store.nextRetryAt?.let { " (next try in ${maxOf(0, (it - System.currentTimeMillis()) / 1000)} s)" } ?: "")
      "connecting" -> "Connecting…"
      else -> "Not connected" + (store.lastError?.let { ": $it" } ?: "")
    }
    val open = PendingIntent.getActivity(
      ctx, NotifyIds.CONNECTION,
      Intent(Intent.ACTION_VIEW, Uri.parse("switchyard://settings/notifications")).setPackage(ctx.packageName)
        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
      flags(),
    )
    return NotificationCompat.Builder(ctx, Channels.CONNECTION)
      .setSmallIcon(icon())
      .setContentTitle("Switchyard")
      .setContentText(text)
      .setOngoing(true)
      .setOnlyAlertOnce(true)
      .setShowWhen(false)
      .setPriority(NotificationCompat.PRIORITY_MIN)
      .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
      .setContentIntent(open)
      .build()
  }

  /** 4403 and the other stops: one plain notification saying why. */
  fun stopped(why: String) {
    ensureChannels()
    notify(
      NotifyIds.forTag("sy-link-stopped"),
      NotificationCompat.Builder(ctx, Channels.NEEDS_YOU).setSmallIcon(icon()).setContentTitle("Switchyard").setContentText(why)
        .setAutoCancel(true).build(),
    )
  }

  /** sendLocalTest(): a notification on sy_test without the hub. */
  fun localTest() {
    ensureChannels()
    post(
      AlertNote(
        "test", "sy-test-local", "Switchyard test", "This phone can show notifications.", Channels.TEST,
        System.currentTimeMillis(), "/#/", null, "session", null, null, null, null, 0,
      ),
    )
  }

  private fun notify(id: Int, n: Notification) {
    try {
      nm.notify(id, n)
    } catch (e: SecurityException) {
      Log.w(LinkClient.TAG, "notifications not allowed")
    }
  }

  companion object {
    fun sameOrigin(a: String, b: String): Boolean = norm(a) == norm(b)

    private fun norm(o: String) = o.lowercase().trimEnd('/').removeSuffix(":443")

    fun ago(since: Long): String {
      val m = (System.currentTimeMillis() - since) / 60_000
      return when {
        m < 1 -> "just now"
        m < 60 -> "$m min"
        else -> "${m / 60} h ${m % 60} min"
      }
    }
  }
}
