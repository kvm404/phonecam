package com.kvm404.phonecam.streaming

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test
import java.nio.ByteBuffer

/**
 * JVM tests for the pure stride-copy core using synthetic YUV_420_888-style buffers.
 */
class FrameConverterTest {

    @Test
    fun `copyPlane with row padding tightly packs each row`() {
        // width=4, height=2, rowStride=6 (2 bytes of trailing padding per row).
        val width = 4
        val height = 2
        val rowStride = 6
        val buf = ByteBuffer.wrap(
            byteArrayOf(
                1, 2, 3, 4, /* pad */ -1, -1,
                5, 6, 7, 8, /* pad */ -1, -1,
            )
        )
        val out = FrameConverter.copyPlane(buf, width, height, rowStride, pixelStride = 1)
        assertArrayEquals(byteArrayOf(1, 2, 3, 4, 5, 6, 7, 8), out)
        // Source position must be untouched (absolute gets).
        assertEquals(0, buf.position())
    }

    @Test
    fun `copyPlane with pixelStride 2 extracts every other byte`() {
        // Interleaved chroma: width=2, height=2, pixelStride=2, rowStride=5 (1 pad byte).
        val width = 2
        val height = 2
        val rowStride = 5
        val buf = ByteBuffer.wrap(
            byteArrayOf(
                10, 99, 11, 99, /* pad */ -1,
                12, 99, 13, 99, /* pad */ -1,
            )
        )
        val out = FrameConverter.copyPlane(buf, width, height, rowStride, pixelStride = 2)
        assertArrayEquals(byteArrayOf(10, 11, 12, 13), out)
    }

    @Test
    fun `toFrameData packs Y U V planes with padding and interleaved chroma`() {
        val width = 4
        val height = 2
        // Y: rowStride 6, planar.
        val yBuf = ByteBuffer.wrap(
            byteArrayOf(
                1, 2, 3, 4, -1, -1,
                5, 6, 7, 8, -1, -1,
            )
        )
        // Chroma dims: 2x1. Interleaved (pixelStride 2), rowStride 4.
        val uBuf = ByteBuffer.wrap(byteArrayOf(20, 30, 21, 31)) // U at 0,2
        val vBuf = ByteBuffer.wrap(byteArrayOf(40, 50, 41, 51)) // V at 0,2

        val frame = FrameConverter.toFrameData(
            width = width,
            height = height,
            yBuffer = yBuf,
            yRowStride = 6,
            yPixelStride = 1,
            uBuffer = uBuf,
            uRowStride = 4,
            uPixelStride = 2,
            vBuffer = vBuf,
            vRowStride = 4,
            vPixelStride = 2,
            timestampUs = 123456L,
        )

        assertEquals(width, frame.width)
        assertEquals(height, frame.height)
        assertEquals(123456L, frame.timestampUs)
        assertArrayEquals(byteArrayOf(1, 2, 3, 4, 5, 6, 7, 8), frame.y)
        assertArrayEquals(byteArrayOf(20, 21), frame.u)
        assertArrayEquals(byteArrayOf(40, 41), frame.v)
    }

    @Test
    fun toFrameDataCenterCropsOversizedFrames() {
        // Y: 8x4, tightly packed (rowStride 8), value = row*10 + col.
        val y = ByteArray(8 * 4) { i -> ((i / 8) * 10 + (i % 8)).toByte() }
        // Chroma: 4x2 planar (pixelStride 1, rowStride 4), value = 100 + row*10 + col (U), 50 more (V).
        val u = ByteArray(4 * 2) { i -> (100 + (i / 4) * 10 + (i % 4)).toByte() }
        val v = ByteArray(4 * 2) { i -> (50 + (i / 4) * 10 + (i % 4)).toByte() }

        val frame = FrameConverter.toFrameData(
            width = 8,
            height = 4,
            yBuffer = ByteBuffer.wrap(y),
            yRowStride = 8,
            yPixelStride = 1,
            uBuffer = ByteBuffer.wrap(u),
            uRowStride = 4,
            uPixelStride = 1,
            vBuffer = ByteBuffer.wrap(v),
            vRowStride = 4,
            vPixelStride = 1,
            timestampUs = 42L,
            targetWidth = 4,
            targetHeight = 2,
        )

        assertEquals(4, frame.width)
        assertEquals(2, frame.height)
        // Even-rounded offsets: top = ((4-2)/2) rounded down to even = 0, left = ((8-4)/2) = 2.
        assertArrayEquals(byteArrayOf(2, 3, 4, 5, 12, 13, 14, 15), frame.y)
        // Chroma: 2x1 from offsets top/2 = 0, left/2 = 1.
        assertArrayEquals(byteArrayOf(101, 102), frame.u)
        assertArrayEquals(byteArrayOf(51, 52), frame.v)
    }

    // ---- rotation ----

    /**
     * Build a FrameData whose Y plane is [w]x[h] with value = row*10+col, and 2x-smaller
     * chroma planes with distinct value bases (u from 40, v from 80) so cross-plane bugs are
     * visible and every value stays inside the signed-byte range.
     */
    private fun frame(w: Int, h: Int, ts: Long = 7L): FrameData {
        val y = ByteArray(w * h) { i -> ((i / w) * 10 + (i % w)).toByte() }
        val cw = w / 2
        val ch = h / 2
        val u = ByteArray(cw * ch) { i -> (40 + (i / cw) * 10 + (i % cw)).toByte() }
        val v = ByteArray(cw * ch) { i -> (80 + (i / cw) * 10 + (i % cw)).toByte() }
        return FrameData(w, h, y, u, v, ts)
    }

    @Test
    fun `rotate 0 is a no-op returning the same instance`() {
        val f = frame(4, 2)
        val r = FrameConverter.rotate(f, 0)
        assertEquals(f, r)
    }

    @Test
    fun `rotate 90 swaps dimensions and rotates all planes clockwise`() {
        // Y 4x4 rows: 0..3 / 10..13 / 20..23 / 30..33.  u 2x2: 40 41 / 50 51.  v: 80 81 / 90 91.
        val f = frame(4, 4, ts = 99L)
        val r = FrameConverter.rotate(f, 90)

        assertEquals(4, r.width)
        assertEquals(4, r.height)
        assertEquals(99L, r.timestampUs)
        // 90 CW: bottom row becomes left column.
        assertArrayEquals(
            byteArrayOf(30, 20, 10, 0, 31, 21, 11, 1, 32, 22, 12, 2, 33, 23, 13, 3),
            r.y,
        )
        assertArrayEquals(byteArrayOf(50, 40, 51, 41), r.u)
        assertArrayEquals(byteArrayOf(90, 80, 91, 81), r.v)
    }

    @Test
    fun `rotate 180 keeps dimensions and reverses every plane`() {
        val f = frame(4, 4)
        val r = FrameConverter.rotate(f, 180)

        assertEquals(4, r.width)
        assertEquals(4, r.height)
        assertArrayEquals(
            byteArrayOf(33, 32, 31, 30, 23, 22, 21, 20, 13, 12, 11, 10, 3, 2, 1, 0),
            r.y,
        )
        assertArrayEquals(byteArrayOf(51, 50, 41, 40), r.u)
        assertArrayEquals(byteArrayOf(91, 90, 81, 80), r.v)
    }

    @Test
    fun `rotate 270 swaps dimensions and rotates all planes counter-clockwise`() {
        val f = frame(4, 4)
        val r = FrameConverter.rotate(f, 270)

        assertEquals(4, r.width)
        assertEquals(4, r.height)
        // 270 CW: top row becomes left column bottom-to-top.
        assertArrayEquals(
            byteArrayOf(3, 13, 23, 33, 2, 12, 22, 32, 1, 11, 21, 31, 0, 10, 20, 30),
            r.y,
        )
        assertArrayEquals(byteArrayOf(41, 51, 40, 50), r.u)
        assertArrayEquals(byteArrayOf(81, 91, 80, 90), r.v)
    }

    @Test
    fun `rotate 90 on a non-square frame swaps width and height`() {
        // Y 4x2:  0 1 2 3 / 10 11 12 13.
        val f = frame(4, 2)
        val r = FrameConverter.rotate(f, 90)

        assertEquals(2, r.width)
        assertEquals(4, r.height)
        // 90 CW: bottom row (10..13) becomes the left column.
        assertArrayEquals(byteArrayOf(10, 0, 11, 1, 12, 2, 13, 3), r.y)
    }

    @Test
    fun `rotate 90 then 270 round-trips to the original`() {
        // Non-square to catch dimension bugs.
        val f = frame(8, 4)
        val back = FrameConverter.rotate(FrameConverter.rotate(f, 90), 270)
        assertEquals(f.width, back.width)
        assertEquals(f.height, back.height)
        assertArrayEquals(f.y, back.y)
        assertArrayEquals(f.u, back.u)
        assertArrayEquals(f.v, back.v)
    }

    @Test
    fun `rotate 180 twice round-trips to the original`() {
        val f = frame(8, 4)
        val back = FrameConverter.rotate(FrameConverter.rotate(f, 180), 180)
        assertEquals(f.width, back.width)
        assertEquals(f.height, back.height)
        assertArrayEquals(f.y, back.y)
        assertArrayEquals(f.u, back.u)
        assertArrayEquals(f.v, back.v)
    }

    @Test
    fun `rotate rejects unsupported angles`() {
        try {
            FrameConverter.rotate(frame(4, 2), 45)
            org.junit.Assert.fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            org.junit.Assert.assertTrue(e.message!!.contains("unsupported rotation"))
        }
    }

    @Test
    fun toFrameDataRejectsUndersizedFrames() {
        val buf = ByteBuffer.wrap(ByteArray(64))
        try {
            FrameConverter.toFrameData(
                width = 4,
                height = 4,
                yBuffer = buf,
                yRowStride = 4,
                yPixelStride = 1,
                uBuffer = buf,
                uRowStride = 2,
                uPixelStride = 1,
                vBuffer = buf,
                vRowStride = 2,
                vPixelStride = 1,
                timestampUs = 0L,
                targetWidth = 8,
                targetHeight = 8,
            )
            org.junit.Assert.fail("expected IllegalArgumentException")
        } catch (e: IllegalArgumentException) {
            org.junit.Assert.assertTrue(e.message!!.contains("smaller than encoder target"))
        }
    }
}
