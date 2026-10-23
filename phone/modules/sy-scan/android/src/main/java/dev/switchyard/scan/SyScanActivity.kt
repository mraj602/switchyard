package dev.switchyard.scan

import com.journeyapps.barcodescanner.CaptureActivity

/** zxing's scanner screen under its own manifest entry, so it can be held in portrait. */
class SyScanActivity : CaptureActivity()
