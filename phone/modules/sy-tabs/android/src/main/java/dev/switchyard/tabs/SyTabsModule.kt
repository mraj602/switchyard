package dev.switchyard.tabs

import android.content.Context
import android.content.SharedPreferences
import android.os.Handler
import android.os.Looper
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

    OnCreate {
      store = appContext.reactContext?.getSharedPreferences("sy-tabs", Context.MODE_PRIVATE)
      main.post { applyPending() } // before this process has made any WebView
    }

    AsyncFunction("listProfiles") {
      if (!supported()) emptyList()
      else TabPolicy.listed(ProfileStore.getInstance().allProfileNames.toList(), pending())
    }.runOnQueue(Queues.MAIN)

    AsyncFunction("deleteProfile") { seat: String ->
      val name = TabPolicy.profileName(seat) ?: throw CodedException("ERR_SY_TABS_SEAT", "sy-tabs: not a seat id", null)
      if (!supported()) return@AsyncFunction false
      if (name in open) throw CodedException("ERR_SY_TABS_IN_USE", "sy-tabs: close the tab first", null)
      try {
        ProfileStore.getInstance().deleteProfile(name).also { setPending(name, false) }
      } catch (e: IllegalStateException) {
        // WebView keeps the profile of a closed (destroyed) tab "in use" for the rest of the
        // process (found on the emulator: the delete failed with no tab open, even after a GC).
        // Wipe what a sign-in leaves, hide it, and delete it for real when the app next starts.
        wipe(name)
        setPending(name, true)
        true
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
    private val main = Handler(Looper.getMainLooper())
    private var store: SharedPreferences? = null

    /** Profiles with a live tab view (main thread only). */
    private val open = HashSet<String>()

    private const val PENDING = "pending"

    private fun pending(): Set<String> = store?.getStringSet(PENDING, emptySet())?.toSet() ?: emptySet()

    private fun setPending(name: String, on: Boolean) {
      val next = pending().let { if (on) it + name else it - name }
      store?.edit()?.putStringSet(PENDING, next)?.apply()
    }

    /** A tab view bound the profile: it is in use, and no longer waiting to be deleted. */
    fun opened(name: String) {
      open.add(name)
      setPending(name, false)
    }

    fun closed(name: String) {
      open.remove(name)
    }

    /** Signed-out profiles that could not be deleted live: delete them now, before any WebView exists. */
    private fun applyPending() {
      if (!supported()) return
      for (name in pending()) {
        try {
          ProfileStore.getInstance().deleteProfile(name)
          setPending(name, false)
        } catch (_: Throwable) {
        }
      }
    }

    private fun wipe(name: String) {
      try {
        val p = ProfileStore.getInstance().getProfile(name) ?: return
        p.cookieManager.removeAllCookies(null)
        p.webStorage.deleteAllData()
        p.cookieManager.flush()
      } catch (_: Throwable) {
      }
    }

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
