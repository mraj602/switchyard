package dev.switchyard.link

import android.app.AlarmManager
import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build

/**
 * One alarm that redraws the Android widgets at the next limit reset (phone.md P11). The widget
 * library cannot tick or schedule a time, so a reset ring that read "resets 15:48" would stay
 * stale until the 30-minute period; this fires at the reset and asks every widget of this app to
 * update. The update runs the widget task handler (no network), which draws the new state and
 * schedules the next reset. A rough alarm (setAndAllowWhileIdle): a few minutes late is fine.
 */
object WidgetTick {
  const val ACTION = "dev.switchyard.link.WIDGET_TICK"
  private const val REQUEST = 7301

  private fun pending(ctx: Context): PendingIntent =
    PendingIntent.getBroadcast(
      ctx, REQUEST,
      Intent(ctx, WidgetTickReceiver::class.java).setAction(ACTION),
      PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
    )

  /** At `atMs` (epoch ms); 0 or less cancels. */
  fun schedule(ctx: Context, atMs: Long) {
    val am = ctx.getSystemService(Context.ALARM_SERVICE) as? AlarmManager ?: return
    if (atMs <= 0) am.cancel(pending(ctx)) else am.setAndAllowWhileIdle(AlarmManager.RTC, atMs, pending(ctx))
  }

  /** An update broadcast to every widget provider of this app that has a widget placed. */
  fun refreshAll(ctx: Context) {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
    val mgr = AppWidgetManager.getInstance(ctx)
    for (info in mgr.getInstalledProvidersForPackage(ctx.packageName, null)) {
      val ids = mgr.getAppWidgetIds(info.provider)
      if (ids.isEmpty()) continue
      ctx.sendBroadcast(
        Intent(AppWidgetManager.ACTION_APPWIDGET_UPDATE)
          .setComponent(info.provider)
          .putExtra(AppWidgetManager.EXTRA_APPWIDGET_IDS, ids),
      )
    }
  }
}

class WidgetTickReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    if (intent.action == WidgetTick.ACTION) WidgetTick.refreshAll(ctx)
  }
}
