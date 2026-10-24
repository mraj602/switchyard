package dev.watchyard.scan

import com.journeyapps.barcodescanner.CaptureActivity

/** zxing's scanner screen under its own manifest entry, so it can be held in portrait. */
class WyScanActivity : CaptureActivity()
