package dev.switchyard.tabs

import androidx.webkit.ProfileStore
import androidx.webkit.WebViewFeature
import expo.modules.kotlin.exception.CodedException
import expo.modules.kotlin.functions.Queues
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

/**
 * phone.md P17.1: the profile list, sign-out (delete a profile) and the tab view. Every profile
 * call runs on the main thread, as the WebView profile API requires. Nothing here reads cookies
 * or storage: the module only creates, flushes and deletes profiles.
 */
class SyTabsModule : Module() {
  override fun definition() = ModuleDefinition {
    Name("SyTabs")

    AsyncFunction("isSupported") { supported() }.runOnQueue(Queues.MAIN)

    AsyncFunction("listProfiles") {
      if (!supported()) emptyList() else ProfileStore.getInstance().allProfileNames.mapNotNull(TabPolicy::seatOf).sorted()
    }.runOnQueue(Queues.MAIN)

    AsyncFunction("deleteProfile") { seat: String ->
      val name = TabPolicy.profileName(seat) ?: throw CodedException("ERR_SY_TABS_SEAT", "sy-tabs: not a seat id", null)
      if (!supported()) return@AsyncFunction false
      try {
        ProfileStore.getInstance().deleteProfile(name)
      } catch (e: IllegalStateException) {
        throw CodedException("ERR_SY_TABS_IN_USE", "sy-tabs: close the tab first", e)
      }
    }.runOnQueue(Queues.MAIN)

    // Cookies reach the disk when the app goes to the background (and when a tab closes).
    OnActivityEntersBackground { flushAll() }

    View(SyTabView::class) {
      Events("onTabState")
      Prop("seat") { view: SyTabView, seat: String? -> view.seat = seat }
      Prop("url") { view: SyTabView, url: String? -> view.url = url }
      OnViewDidUpdateProps { view: SyTabView -> view.update() }
      OnViewDestroys { view: SyTabView -> view.destroy() }
    }
  }

  companion object {
    fun supported(): Boolean =
      try {
        WebViewFeature.isFeatureSupported(WebViewFeature.MULTI_PROFILE)
      } catch (_: Throwable) {
        false // no WebView package, or one that cannot be loaded
      }

    fun flush(profile: String) {
      try {
        ProfileStore.getInstance().getProfile(profile)?.cookieManager?.flush()
      } catch (_: Throwable) {
      }
    }

    fun flushAll() {
      if (!supported()) return
      try {
        ProfileStore.getInstance().allProfileNames.filter { TabPolicy.seatOf(it) != null }.forEach { flush(it) }
      } catch (_: Throwable) {
      }
    }
  }
}
