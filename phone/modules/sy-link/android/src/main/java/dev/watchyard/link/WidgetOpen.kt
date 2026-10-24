package dev.watchyard.link

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log

/**
 * Widget taps (phone.md P11). react-native-android-widget opens an OPEN_URI tap with an implicit
 * ACTION_VIEW, which any app that registers the watchyard:// scheme could answer. The widget
 * providers (plugins/with-sy-native.js) hand the tap to [handle] first, which starts this app's own
 * launcher activity at the link by component name, and only for a watchyard:// link.
 */
object WidgetOpen {
  private const val MAX_LINK = 512

  /** Only a short watchyard:// link with a host is opened; anything else is dropped. */
  fun openable(uri: String?): Boolean =
    uri != null && uri.length <= MAX_LINK && uri.startsWith("watchyard://") && uri.length > "watchyard://".length

  /** The app's own launcher activity at [link], by component, so no chooser and no other app. */
  fun appIntent(ctx: Context, link: String): Intent {
    val i = Intent(Intent.ACTION_VIEW, Uri.parse(link)).setPackage(ctx.packageName)
    ctx.packageManager.getLaunchIntentForPackage(ctx.packageName)?.component?.let { i.setComponent(it) }
    return i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
  }

  /**
   * True when [intent] is an OPEN_URI widget tap (it is then fully handled, even when refused);
   * false for every other broadcast, which the library handles.
   */
  @JvmStatic
  fun handle(ctx: Context, intent: Intent): Boolean {
    val action = intent.action ?: return false
    if (!action.startsWith(ctx.packageName + ".WIDGET")) return false
    if (intent.getStringExtra("clickAction") != "OPEN_URI") return false
    val uri = intent.getBundleExtra("clickActionData")?.getString("uri")
    if (!openable(uri)) {
      Log.w(LinkClient.TAG, "widget tap refused")
      return true
    }
    try {
      ctx.startActivity(appIntent(ctx, uri!!))
    } catch (e: Exception) {
      Log.w(LinkClient.TAG, "widget tap: ${e.javaClass.simpleName}")
    }
    return true
  }
}
