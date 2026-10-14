package dev.switchyard.link

import android.content.Context
import android.util.Log
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

/**
 * push.md 14.4 "Watchdog": every 15 minutes (`KEEP`). With instant delivery on, start the service
 * when it is not running (Android allows that from the background only with the battery
 * exemption, 14.2); when the start is refused, or with instant delivery off, do the 15-minute
 * check of 14.9: one long-poll login, process what is queued, ack, disconnect.
 */
class Watchdog(ctx: Context, params: WorkerParameters) : Worker(ctx, params) {
  override fun doWork(): Result {
    val ctx = applicationContext
    val store = Link.store(ctx)
    if (!store.configured || store.verified == "changed" || store.status == "revoked") return Result.success()
    if (store.enabled && SyLinkService.instance != null) return Result.success()
    if (store.enabled && SyLinkService.start(ctx)) return Result.success()
    return try {
      check(ctx, store)
      Result.success()
    } catch (e: Exception) {
      Log.w(LinkClient.TAG, "watchdog check: ${e.javaClass.simpleName}")
      Result.success() // the next period tries again; a retry storm helps nobody
    }
  }

  private fun check(ctx: Context, store: Store) {
    val notifier = Notifier(ctx, store)
    notifier.ensureChannels()
    val events = object : LinkEvents {
      override fun state() = SyLinkModule.emit("state", Link.state(ctx, store))
      override fun stopped(status: String, why: String) = SyLinkService.onStopped(ctx, store, notifier, status, why)
      override fun longTimer(atMs: Long) = Unit
    }
    val client = Link.client(ctx, store, notifier, events)
    try {
      if (client.checkOnce()) store.setError(if (store.enabled) SyLinkService.BACKGROUND_BLOCKED else null)
    } finally {
      client.shutdown()
    }
  }

  companion object {
    const val NAME = "sy-link-watchdog"

    fun schedule(ctx: Context) {
      val req = PeriodicWorkRequestBuilder<Watchdog>(15, TimeUnit.MINUTES).build()
      WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.KEEP, req)
    }

    fun cancel(ctx: Context) {
      WorkManager.getInstance(ctx).cancelUniqueWork(NAME)
    }
  }
}
