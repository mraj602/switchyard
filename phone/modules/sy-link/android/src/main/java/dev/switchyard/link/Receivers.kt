package dev.switchyard.link

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** BOOT_COMPLETED and MY_PACKAGE_REPLACED (push.md 14.4): both are allowed background starts (14.2). */
class BootReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
    val store = Link.store(ctx)
    if (!store.configured || store.verified == "changed" || store.status == "revoked") return
    Watchdog.schedule(ctx)
    if (store.enabled) SyLinkService.start(ctx)
  }
}

/** The exact alarms of 14.4: the restart after the task was removed, and long reconnect timers. */
class AlarmReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    val store = Link.store(ctx)
    if (!store.configured || !store.enabled || store.verified == "changed" || store.status == "revoked") return
    when (intent.action) {
      RESTART -> if (SyLinkService.instance == null) SyLinkService.start(ctx)
      RETRY -> if (SyLinkService.instance == null) SyLinkService.start(ctx) else SyLinkService.start(ctx, SyLinkService.ACTION_RETRY)
    }
  }

  companion object {
    const val RESTART = "dev.switchyard.link.ALARM_RESTART"
    const val RETRY = "dev.switchyard.link.ALARM_RETRY"
  }
}

/**
 * Snooze and Seen (push.md 14.6), with the app killed. The notification goes at once; then the
 * link, the action URL, the local queue. While the service runs it does the work (no time
 * limit); otherwise the receiver keeps itself alive with goAsync for at most 9 s (HTTP only:
 * there is no link without the service) and queues what did not get through.
 */
class ActionReceiver : BroadcastReceiver() {
  override fun onReceive(ctx: Context, intent: Intent) {
    val action = intent.getStringExtra(EXTRA_ACTION) ?: return
    val token = intent.getStringExtra(EXTRA_TOKEN) ?: return
    if (action != "snooze" && action != "seen") return
    val tag = intent.getStringExtra(EXTRA_TAG) ?: ""
    Notifier(ctx, Link.store(ctx)).dismiss(tag)
    val a = PendingAction(
      LinkClient.newActionId(), token, action, intent.getStringExtra(EXTRA_ALERT) ?: "",
      intent.getStringExtra(EXTRA_ORIGIN) ?: Link.store(ctx).origin ?: "", System.currentTimeMillis(),
    )
    val svc = SyLinkService.instance
    if (svc != null) return svc.runAction(a)
    val pending = goAsync()
    Thread({
      try {
        val transport = object : ActionTransport {
          override fun viaLink(id: String, token: String, timeoutMs: Long): Int? = null
          override fun viaHttp(origin: String, token: String, timeoutMs: Long): Int? = LinkClient.postAction(origin, token, timeoutMs)
        }
        ActionRunner(Link.store(ctx), transport, System::currentTimeMillis).run(a, budgetMs = RECEIVER_BUDGET_MS)
      } finally {
        pending.finish()
      }
    }, "sy-link-action").start()
  }

  companion object {
    const val EXTRA_ACTION = "sy.action"
    const val EXTRA_TOKEN = "sy.token"
    const val EXTRA_ALERT = "sy.alert"
    const val EXTRA_TAG = "sy.tag"
    const val EXTRA_ORIGIN = "sy.origin"
    const val RECEIVER_BUDGET_MS = 9_000L
  }
}
