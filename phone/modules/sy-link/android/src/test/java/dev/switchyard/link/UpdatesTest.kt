package dev.switchyard.link

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private val H_A = "a".repeat(64)
private val H_B = "b".repeat(64)

private fun manifestJson(version: String = "0.9.0", build: Int = 900, minSdk: Int = 31, sha: String = H_A, cert: String = H_B, extra: String = "") =
  """{"v":1,"version":"$version","build":$build,"min_sdk":$minSdk,"apk":"switchyard-$version.apk","size":10,"sha256":"$sha","cert_sha256":"$cert","notes":"fixes"$extra}"""

class VersionCompareTest {
  @Test fun comparesNumericallyNotAsText() {
    assertTrue(Updates.compareVersions("0.10.0", "0.9.0") > 0)
    assertTrue(Updates.compareVersions("1.0.0", "0.99.99") > 0)
    assertTrue(Updates.compareVersions("0.8.1", "0.8.2") < 0)
    assertEquals(0, Updates.compareVersions("2.3.4", "2.3.4"))
  }

  @Test fun unparsableVersionsSortLowest() {
    assertTrue(Updates.compareVersions("junk", "0.0.1") < 0)
    assertTrue(Updates.compareVersions("0.0.1", "1.0") > 0)
    assertNull(Updates.parseVersion("1.2"))
    assertNull(Updates.parseVersion("1.2.x"))
    assertNull(Updates.parseVersion("1.-2.3"))
  }

  @Test fun offeredOnlyWhenBuildIsHigherAndSdkFits() {
    val m = Updates.parseManifest(manifestJson(build = 900, minSdk = 31))!!
    assertTrue(Updates.isOffered(m, 800, 34))
    assertFalse(Updates.isOffered(m, 900, 34)) // same build
    assertFalse(Updates.isOffered(m, 901, 34)) // a newer install
    assertFalse(Updates.isOffered(m, 800, 30)) // phone older than min_sdk
    assertTrue(Updates.isOffered(m, 800, 31))
  }
}

class ManifestParsingTest {
  @Test fun readsTheP16Fields() {
    val m = Updates.parseManifest(manifestJson(extra = ""","published_at":"2026-10-15T00:00:00Z","future":1"""))!!
    assertEquals("0.9.0", m.version)
    assertEquals(900L, m.build)
    assertEquals(31, m.minSdk)
    assertEquals("switchyard-0.9.0.apk", m.apk)
    assertEquals(H_A, m.sha256)
    assertEquals(H_B, m.certSha256)
    assertEquals("fixes", m.notes)
  }

  @Test fun refusesWhatTheHubMirrorRefuses() {
    val good = manifestJson()
    assertNotNull(Updates.parseManifest(good))
    for (bad in listOf(
      good.replace("\"v\":1", "\"v\":2"),
      good.replace("switchyard-0.9.0.apk", "../x.apk"),
      good.replace(H_A, "AB"),
      good.replace(H_A, H_A.uppercase()),
      good.replace(H_B, "c".repeat(63)),
      good.replace("\"build\":900", "\"build\":0"),
      good.replace("\"version\":\"0.9.0\"", "\"version\":\"nine\""),
      "{",
      "[]",
    )) assertNull(bad, Updates.parseManifest(bad))
  }

  @Test fun hubAnswerResolvesItsUrlAgainstThePinnedOrigin() {
    val body = manifestJson(extra = ""","url":"/v1/app/android/apk"""")
    val (m, url) = Updates.hubOffer("https://hub.ts.net/", body)!!
    assertEquals("https://hub.ts.net/v1/app/android/apk", url)
    assertEquals(900L, m.build)
    // a url that points elsewhere is not followed
    assertNull(Updates.hubOffer("https://hub.ts.net", manifestJson(extra = ""","url":"https://evil.example/x.apk"""")))
    assertNull(Updates.hubOffer("https://hub.ts.net", manifestJson()))
  }

  @Test fun theTokenGoesOnlyToThePairedOrigin() {
    assertTrue(Updates.sendsToken("https://hub.ts.net/v1/app/android/apk", "https://hub.ts.net"))
    assertTrue(Updates.sendsToken("https://hub.ts.net/v1/app/android/apk", "https://hub.ts.net/"))
    assertFalse(Updates.sendsToken("https://hub.ts.net.evil.example/x", "https://hub.ts.net"))
    assertFalse(Updates.sendsToken("https://github.com/x/y/releases/download/a.apk", "https://hub.ts.net"))
    assertFalse(Updates.sendsToken("https://hub.ts.net/x", null))
  }

  @Test fun offerRoundTripsThroughTheJsMap() {
    val m = Updates.parseManifest(manifestJson())!!
    val back = UpdateOffer.fromMap(UpdateOffer(m, "https://h/apk", "github").toMap())!!
    assertEquals(m.sha256, back.manifest.sha256)
    assertEquals(m.certSha256, back.manifest.certSha256)
    assertEquals(900L, back.manifest.build)
    assertEquals("github", back.source)
    assertNull(UpdateOffer.fromMap(mapOf("version" to "0.9.0")))
    assertNull(UpdateOffer.fromMap(UpdateOffer(m, "https://h/apk", "github").toMap() + ("sha256" to "zz")))
    assertNull(UpdateOffer.fromMap(UpdateOffer(m, "https://h/apk", "ftp").toMap()))
  }
}

class ReleaseFilterTest {
  private fun rel(tag: String, draft: Boolean = false, pre: Boolean = false) =
    """{"tag_name":"$tag","draft":$draft,"prerelease":$pre,"assets":[{"name":"manifest.json","browser_download_url":"https://dl/$tag/manifest.json"}]}"""

  @Test fun picksTheHighestPublishedPhoneTagByNumber() {
    val json = "[" + listOf(
      rel("phone-v0.8.0"), rel("phone-v0.10.0"), rel("phone-v0.11.0", draft = true),
      rel("phone-v0.12.0", pre = true), rel("v9.9.9"), rel("phone-v1.0"), rel("phone-v0.9.0"),
    ).joinToString(",") + "]"
    val r = Updates.newestRelease(json)!!
    assertEquals("phone-v0.10.0", r.tag)
    assertEquals("https://dl/phone-v0.10.0/manifest.json", r.assets["manifest.json"])
  }

  @Test fun nothingMatchesNothingOffered() {
    assertNull(Updates.newestRelease("[" + rel("v1.0.0") + "]"))
    assertNull(Updates.newestRelease("not json"))
    assertNull(Updates.newestRelease("[]"))
    // numbers too large for an Int are skipped, not thrown
    assertEquals("phone-v0.9.0", Updates.newestRelease("[" + rel("phone-v99999999999.0.0") + "," + rel("phone-v0.9.0") + "]")!!.tag)
  }
}

/** A scripted PackageManager. */
private class FakeSource(var archive: ArchiveFacts?, var installed: ArchiveFacts?) : SigningSource {
  override fun archive(path: String) = archive
  override fun installed() = installed
}

class CertMatchTest {
  private val key = "my release key".toByteArray()
  private val other = "somebody else".toByteArray()
  private val keyHex = Codec.hex(Codec.sha256(key))

  private fun facts(vararg signers: ByteArray, pkg: String = "dev.switchyard.app", code: Long = 900) = ArchiveFacts(pkg, code, signers.toList())

  @Test fun acceptsAnUpdateSignedByTheManifestAndInstalledKey() {
    val s = FakeSource(facts(key, code = 900), facts(key, code = 800))
    assertEquals(Verdict.OK, CertCheck.verify(s, "x.apk", keyHex))
    assertEquals(Verdict.OK, CertCheck.verify(s, "x.apk", keyHex.uppercase())) // the manifest's case does not matter
  }

  @Test fun refusesAKeyThatIsNotTheManifests() {
    val s = FakeSource(facts(other), facts(key, code = 800))
    assertEquals(Verdict.MANIFEST_CERT_MISMATCH, CertCheck.verify(s, "x.apk", keyHex))
  }

  @Test fun refusesWhenTheManifestMatchesButTheInstalledAppUsesAnotherKey() {
    // a hub or release that vouches for a key the installed app was never signed with
    val s = FakeSource(facts(other), facts(key, code = 800))
    assertEquals(Verdict.INSTALLED_CERT_MISMATCH, CertCheck.verify(s, "x.apk", Codec.hex(Codec.sha256(other))))
  }

  @Test fun aMultiSignerArchiveMatchesOnlyItsWholeSet() {
    val s = FakeSource(facts(key, other), facts(key, code = 800))
    assertEquals(Verdict.MANIFEST_CERT_MISMATCH, CertCheck.verify(s, "x.apk", keyHex))
  }

  @Test fun refusesUnsignedUnreadableWrongPackageAndNotNewer() {
    val installed = facts(key, code = 800)
    assertEquals(Verdict.UNSIGNED, CertCheck.verify(FakeSource(facts(), installed), "x.apk", keyHex))
    assertEquals(Verdict.NOT_AN_APK, CertCheck.verify(FakeSource(null, installed), "x.apk", keyHex))
    assertEquals(Verdict.WRONG_PACKAGE, CertCheck.verify(FakeSource(facts(key, pkg = "evil.app"), installed), "x.apk", keyHex))
    assertEquals(Verdict.NOT_NEWER, CertCheck.verify(FakeSource(facts(key, code = 800), installed), "x.apk", keyHex))
    assertEquals(Verdict.INSTALLED_CERT_MISMATCH, CertCheck.verify(FakeSource(facts(key), null), "x.apk", keyHex))
  }
}

/** The hub first, then GitHub (phone.md P16.3). */
class UpdateCheckTest {
  private class Net(val answers: Map<String, Fetched>) : Fetcher {
    val calls = mutableListOf<Pair<String, String?>>()
    override fun get(url: String, token: String?): Fetched {
      calls += url to token
      return answers.entries.firstOrNull { url.startsWith(it.key) }?.value ?: Fetched(0, null)
    }
    override fun download(url: String, token: String?, dest: java.io.File, maxBytes: Long): String? = null
  }

  private val hubUrl = "https://hub.ts.net/v1/app/android"
  private val hubBody = manifestJson(build = 900, extra = ""","url":"/v1/app/android/apk"""")
  private val ghList = """[{"tag_name":"phone-v0.9.0","assets":[
    {"name":"manifest.json","browser_download_url":"https://gh/m.json"},
    {"name":"switchyard-0.9.0.apk","browser_download_url":"https://gh/a.apk"}]}]"""
  private val gh = "https://api.github.com/repos/mraj602/switchyard/releases"

  @Test fun theHubAnswersFirstAndGitHubIsNotAsked() {
    val net = Net(mapOf(hubUrl to Fetched(200, hubBody)))
    val o = UpdateClient(net).check("https://hub.ts.net", "tok", 800, 34)!!
    assertEquals("hub", o.source)
    assertEquals("https://hub.ts.net/v1/app/android/apk", o.url)
    assertEquals(listOf(hubUrl to "tok"), net.calls)
  }

  @Test fun aHubThatIsUpToDateEndsTheCheck() {
    val net = Net(mapOf(hubUrl to Fetched(200, hubBody)))
    assertNull(UpdateClient(net).check("https://hub.ts.net", "tok", 900, 34))
    assertEquals(1, net.calls.size)
  }

  @Test fun gitHubIsUsedWhenTheHubHasNoneOrIsUnreachable() {
    for (hub in listOf(Fetched(404, "{}"), Fetched(0, null), Fetched(503, "{}"))) {
      val net = Net(mapOf(hubUrl to hub, gh to Fetched(200, ghList), "https://gh/m.json" to Fetched(200, manifestJson())))
      val o = UpdateClient(net).check("https://hub.ts.net", "tok", 800, 34)!!
      assertEquals("github", o.source)
      assertEquals("https://gh/a.apk", o.url)
      // the hub's token is never sent to GitHub
      assertTrue(net.calls.filter { it.first.contains("gh") || it.first.contains("github") }.all { it.second == null })
    }
  }

  @Test fun aReleaseWhoseManifestDisagreesWithItsTagIsIgnored() {
    val net = Net(mapOf(gh to Fetched(200, ghList), "https://gh/m.json" to Fetched(200, manifestJson(version = "0.9.1"))))
    assertNull(UpdateClient(net).check(null, null, 800, 34))
  }

  @Test fun nothingReachableIsAnErrorNotNoUpdate() {
    try {
      UpdateClient(Net(emptyMap())).check("https://hub.ts.net", "tok", 800, 34)
      throw AssertionError("expected UpdateException")
    } catch (_: UpdateException) {
    }
  }
}

/** installUpdate's order: download, checksum, certificate, then the session (P16.3). */
class InstallTest {
  private val apk = "the real apk".toByteArray()
  private val sha = Codec.hex(Codec.sha256(apk))
  private val key = "my release key".toByteArray()
  private val dir = java.nio.file.Files.createTempDirectory("sy-update").toFile()

  private inner class Net : Fetcher {
    val downloads = mutableListOf<Pair<String, String?>>()
    override fun get(url: String, token: String?) = Fetched(0, null)
    override fun download(url: String, token: String?, dest: java.io.File, maxBytes: Long): String {
      downloads += url to token
      dest.writeBytes(apk)
      return sha
    }
  }

  /** The PackageManager, with an optional hook that runs while it reads the archive. */
  private inner class Source(val during: (java.io.File) -> Unit = {}) : SigningSource {
    override fun archive(path: String): ArchiveFacts {
      during(java.io.File(path))
      return ArchiveFacts("dev.switchyard.app", 900, listOf(key))
    }
    override fun installed() = ArchiveFacts("dev.switchyard.app", 800, listOf(key))
  }

  private fun offer(url: String, source: String = "hub") =
    UpdateOffer(Manifest("0.9.0", 900, 0, "", apk.size.toLong(), sha, Codec.hex(Codec.sha256(key)), ""), url, source)

  private fun installer(net: Net, source: Source, written: java.io.ByteArrayOutputStream) =
    UpdateInstaller(dir, net, source) { f, sha -> f.inputStream().use { Updates.copyChecked(it, written, sha) } }

  @Test fun theSessionGetsTheCheckedBytesAndTheFileIsGone() {
    val written = java.io.ByteArrayOutputStream()
    val net = Net()
    val r = installer(net, Source(), written).install(offer("https://hub.ts.net/v1/app/android/apk"), "https://hub.ts.net", "tok")
    assertTrue(r is UpdateInstaller.Result.Started)
    assertTrue(written.toByteArray().contentEquals(apk))
    assertEquals(listOf("https://hub.ts.net/v1/app/android/apk" to "tok"), net.downloads)
    assertTrue(dir.listFiles()!!.isEmpty())
  }

  @Test fun aFileSwappedAfterItsChecksIsNotInstalled() {
    // TOCTOU: the file changes between the checksum/certificate checks and the session write.
    val written = java.io.ByteArrayOutputStream()
    val r = installer(Net(), Source { it.writeBytes("something else".toByteArray()) }, written)
      .install(offer("https://hub.ts.net/v1/app/android/apk"), "https://hub.ts.net", "tok")
    assertTrue(r is UpdateInstaller.Result.Refused)
    assertTrue(dir.listFiles()!!.isEmpty())
  }

  @Test fun plainHttpOnlyFromThePairedHubItself() {
    // An http hub (development) may serve its own APK; a lookalike host may not, token or not.
    val net = Net()
    val r = installer(net, Source(), java.io.ByteArrayOutputStream())
      .install(offer("http://hub.lan.evil.example/v1/app/android/apk"), "http://hub.lan", "tok")
    assertTrue(r is UpdateInstaller.Result.Refused)
    assertTrue(net.downloads.isEmpty())
    val ok = installer(net, Source(), java.io.ByteArrayOutputStream()).install(offer("http://hub.lan/v1/app/android/apk"), "http://hub.lan", "tok")
    assertTrue(ok is UpdateInstaller.Result.Started)
    assertEquals(listOf("http://hub.lan/v1/app/android/apk" to "tok"), net.downloads)
  }
}
