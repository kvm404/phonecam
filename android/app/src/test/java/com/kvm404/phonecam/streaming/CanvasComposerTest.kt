package com.kvm404.phonecam.streaming

import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test

/**
 * JVM tests for [CanvasComposer]: the fixed-canvas pillarbox composition. Canvas is the base
 * landscape profile; portrait (or otherwise smaller) content must be scaled-to-fit and centered
 * with black bars, and matching content must pass through untouched.
 */
class CanvasComposerTest {

    /** Solid I420 frame of [w]x[h] with the given plane values (chroma at half res). */
    private fun solid(w: Int, h: Int, yVal: Byte, uVal: Byte, vVal: Byte, ts: Long = 5L): FrameData {
        val y = ByteArray(w * h).also { it.fill(yVal) }
        val u = ByteArray((w / 2) * (h / 2)).also { it.fill(uVal) }
        val v = ByteArray((w / 2) * (h / 2)).also { it.fill(vVal) }
        return FrameData(w, h, y, u, v, ts)
    }

    private val neutral = 128.toByte()

    @Test
    fun `matching dims passthrough returns same instance`() {
        val f = solid(1280, 720, 100, 60, 90)
        assertSame(f, CanvasComposer.compose(f, 1280, 720))
    }

    @Test
    fun `portrait 720x1280 pillarboxed onto 1280x720 canvas`() {
        // Solid content so black (16) vs content (100) is trivially distinguishable.
        val content = solid(720, 1280, 100, 60, 90, ts = 42L)
        val out = CanvasComposer.compose(content, 1280, 720)

        assertEquals(1280, out.width)
        assertEquals(720, out.height)
        assertEquals(42L, out.timestampUs)

        // Geometry: scaledHeight=720, scaledWidth=405 rounded down to 404, xOffset=438 (even),
        // so content occupies columns 438..841 with symmetric 438-wide black bars either side.
        val midRow = 360
        assertEquals("top-left corner is black", 16, out.y[0].toInt())
        assertEquals("left bar last column black", 16, out.y[midRow * 1280 + 437].toInt())
        assertEquals("content starts at 438", 100, out.y[midRow * 1280 + 438].toInt())
        assertEquals("content center", 100, out.y[midRow * 1280 + 640].toInt())
        assertEquals("content ends at 841", 100, out.y[midRow * 1280 + 841].toInt())
        assertEquals("right bar starts at 842", 16, out.y[midRow * 1280 + 842].toInt())
        assertEquals("far-right column black", 16, out.y[midRow * 1280 + 1279].toInt())

        // Chroma planes: canvas 640x360, neutral 128 outside the content, source values inside.
        assertEquals(640 * 360, out.u.size)
        assertEquals(640 * 360, out.v.size)
        val chromaMidRow = 180
        assertEquals("chroma corner neutral", neutral, out.u[0])
        assertEquals("chroma U center", 60, out.u[chromaMidRow * 640 + 320].toInt())
        assertEquals("chroma V center", 90, out.v[chromaMidRow * 640 + 320].toInt())
        assertEquals("chroma corner neutral V", neutral, out.v[0])
    }

    @Test
    fun `small 4x8 content nearest-neighbor onto 8x8 canvas is hand-checkable`() {
        // Content Y = row*10 + col (portrait 4 wide, 8 tall). scaledWidth=4 == srcWidth and
        // scaledHeight=8 == srcHeight, so scaling is identity; xOffset=2, yOffset=0.
        val y = ByteArray(4 * 8) { i -> ((i / 4) * 10 + (i % 4)).toByte() }
        val u = ByteArray(2 * 4) { i -> (40 + (i / 2) * 10 + (i % 2)).toByte() }
        val v = ByteArray(2 * 4) { i -> (80 + (i / 2) * 10 + (i % 2)).toByte() }
        val content = FrameData(4, 8, y, u, v, 1L)

        val out = CanvasComposer.compose(content, 8, 8)
        assertEquals(8, out.width)
        assertEquals(8, out.height)

        // Row 0: black black | src[0..3] | black black.
        assertEquals(16, out.y[0].toInt())
        assertEquals(16, out.y[1].toInt())
        assertEquals(0, out.y[2].toInt()) // src[0][0]
        assertEquals(3, out.y[5].toInt()) // src[0][3]
        assertEquals(16, out.y[6].toInt())
        assertEquals(16, out.y[7].toInt())
        // Row 1 content: src[1][0]=10 at col 2, src[1][3]=13 at col 5.
        assertEquals(10, out.y[1 * 8 + 2].toInt())
        assertEquals(13, out.y[1 * 8 + 5].toInt())
        // Bottom row 7: src[7][3] = 73 at col 5.
        assertEquals(73, out.y[7 * 8 + 5].toInt())

        // Chroma canvas 4x4, content 2x4 at col offset 1. Row 0: neutral | u[0]=40 u[1]=41 | neutral.
        assertEquals(neutral, out.u[0])
        assertEquals(40, out.u[1].toInt())
        assertEquals(41, out.u[2].toInt())
        assertEquals(neutral, out.u[3])
        assertEquals(50, out.u[1 * 4 + 1].toInt()) // u[row1][0]
        assertEquals(80, out.v[1].toInt())
    }

    @Test
    fun `odd scaled dims round down to even keeping offsets even`() {
        // Content 6x16 -> canvas 8x8: scaledWidth = 6*8/16 = 3 -> rounds to 2, and the raw
        // xOffset = (8-2)/2 = 3 -> rounds to 2. Content lands on cols 2,3 with even black bars.
        val content = solid(6, 16, 100, 60, 90)
        val out = CanvasComposer.compose(content, 8, 8)

        assertEquals(8, out.width)
        assertEquals(8, out.height)
        // Even offset proof: cols 0,1 black; 2,3 content; 4..7 black.
        assertEquals(16, out.y[0].toInt())
        assertEquals(16, out.y[1].toInt())
        assertEquals(100, out.y[2].toInt())
        assertEquals(100, out.y[3].toInt())
        assertEquals(16, out.y[4].toInt())
        assertEquals(16, out.y[7].toInt())
    }
}
