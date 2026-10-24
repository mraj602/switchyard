package dev.watchyard.link

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PorterDuff
import android.graphics.PorterDuffXfermode
import androidx.core.graphics.PathParser
import java.util.concurrent.ConcurrentHashMap

/**
 * The large notification icon (direction A): the app tile (ink disc, gold mark) with a coloured kind
 * badge bottom-right, a cut-out ring around it so it reads on any card colour. Drawn once per kind
 * at runtime from the same path data as the small icons and cached. Geometry is in the board's 48
 * unit tile space (the badge pokes out to 49, so the canvas is 50 units).
 */
object NotifyIcons {
  private const val UNITS = 50f
  private const val PX = 200 // 4 px per unit: crisp at the 64 dp slot on xxhdpi
  private const val INK = 0xFF17191B.toInt()
  private const val GOLD = 0xFFE8B04B.toInt()
  private const val WHITE = 0xFFFFFFFF.toInt()

  /** Badge fill and glyph colour per kind; GENERIC and CONNECTION have no badge. */
  private val BADGE: Map<IconKind, Pair<Int, Int>> = mapOf(
    IconKind.NEEDS_YOU to (GOLD to INK),
    IconKind.SUMMARY to (GOLD to INK),
    IconKind.FAILED to (0xFFE5534B.toInt() to WHITE),
    IconKind.LIMIT to (0xFFF0883E.toInt() to INK),
    IconKind.RESET to (0xFF3FB68B.toInt() to INK),
    IconKind.MACHINE to (0xFF9AA4AE.toInt() to INK),
  )

  private val cache = ConcurrentHashMap<IconKind, Bitmap>()

  /** The connection note has no large icon; every other kind has the tile. */
  fun large(kind: IconKind): Bitmap? =
    if (kind == IconKind.CONNECTION) null else cache.getOrPut(kind) { render(kind) }

  private fun path(data: String): Path = PathParser.createPathFromPathData(data).apply { fillType = Path.FillType.EVEN_ODD }

  private fun render(kind: IconKind): Bitmap {
    val bmp = Bitmap.createBitmap(PX, PX, Bitmap.Config.ARGB_8888)
    val c = Canvas(bmp)
    val k = PX / UNITS
    c.scale(k, k)
    val p = Paint(Paint.ANTI_ALIAS_FLAG)
    p.color = INK
    c.drawCircle(24f, 24f, 24f, p)
    glyph(c, p, NotifyIconPaths.MARK, GOLD, 10f, 8.5f, 26f)
    val badge = BADGE[kind]
    if (badge != null) {
      val cut = Paint(Paint.ANTI_ALIAS_FLAG).apply { xfermode = PorterDuffXfermode(PorterDuff.Mode.CLEAR) }
      c.drawCircle(38f, 38f, 11f, cut)
      p.color = badge.first
      c.drawCircle(38f, 38f, 9f, p)
      val kindKey = kind.name.lowercase()
      NotifyIconPaths.BADGES[kindKey]?.let { glyph(c, p, it, badge.second, 29.5f, 29.5f, 17f) }
    }
    return bmp
  }

  private fun glyph(c: Canvas, p: Paint, data: String, color: Int, x: Float, y: Float, size: Float) {
    p.color = color
    c.save()
    c.translate(x, y)
    c.scale(size / 24f, size / 24f)
    c.drawPath(path(data), p)
    c.restore()
  }
}
