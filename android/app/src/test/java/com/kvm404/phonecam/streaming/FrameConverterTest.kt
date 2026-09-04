package com.kvm404.phonecam.streaming

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
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
    fun toFrameDataCentersUndersizedFrames() {
        val yBuf = ByteBuffer.wrap(ByteArray(16) { 99.toByte() })
        val uBuf = ByteBuffer.wrap(ByteArray(4) { 50.toByte() })
        val vBuf = ByteBuffer.wrap(ByteArray(4) { 60.toByte() })
        val frame = FrameConverter.toFrameData(
            width = 4,
            height = 4,
            yBuffer = yBuf,
            yRowStride = 4,
            yPixelStride = 1,
            uBuffer = uBuf,
            uRowStride = 2,
            uPixelStride = 1,
            vBuffer = vBuf,
            vRowStride = 2,
            vPixelStride = 1,
            timestampUs = 0L,
            targetWidth = 8,
            targetHeight = 8,
        )
        assertEquals(8, frame.width)
        assertEquals(8, frame.height)
        assertEquals(16.toByte(), frame.y[0]) // top-left border
        assertEquals(99.toByte(), frame.y[2 * 8 + 2]) // centered source at (row=2, col=2)
        // 4x4 into 8x8 → dstTop=dstLeft=2, chroma at (1,1) on a 4×4 plane.
        assertEquals(128.toByte(), frame.u[0])
        assertEquals(128.toByte(), frame.v[0])
        assertEquals(50.toByte(), frame.u[1 * 4 + 1])
        assertEquals(60.toByte(), frame.v[1 * 4 + 1])
    }

    @Test
    fun `copyPlane respects src position offset for interleaved chroma`() {
        val raw = byteArrayOf(
            10, 20, 11, 21,
            12, 22, 13, 23,
        )
        val uBuf = ByteBuffer.wrap(raw)
        uBuf.position(0)
        val vBuf = ByteBuffer.wrap(raw)
        vBuf.position(1)

        val u = FrameConverter.copyPlane(uBuf, width = 2, height = 2, rowStride = 4, pixelStride = 2)
        val v = FrameConverter.copyPlane(vBuf, width = 2, height = 2, rowStride = 4, pixelStride = 2)

        assertArrayEquals(byteArrayOf(10, 11, 12, 13), u)
        assertArrayEquals(byteArrayOf(20, 21, 22, 23), v)
    }

    // ---- flipHorizontallyInPlace ----

    @Test
    fun `flipHorizontallyInPlace swaps left and right columns on non-square YUV frames`() {
        // Test 4x2 frame
        val f42 = frame(4, 2, ts = 100L)
        val result42 = FrameConverter.flipHorizontallyInPlace(f42)
        assertSame(f42, result42)
        assertEquals(4, result42.width)
        assertEquals(2, result42.height)
        assertEquals(100L, result42.timestampUs)
        assertArrayEquals(
            byteArrayOf(3, 2, 1, 0, 13, 12, 11, 10),
            result42.y,
        )
        assertArrayEquals(byteArrayOf(41, 40), result42.u)
        assertArrayEquals(byteArrayOf(81, 80), result42.v)

        // Test 8x4 frame
        val f84 = frame(8, 4, ts = 200L)
        val result84 = FrameConverter.flipHorizontallyInPlace(f84)
        assertSame(f84, result84)
        assertEquals(8, result84.width)
        assertEquals(4, result84.height)
        assertEquals(200L, result84.timestampUs)
        assertArrayEquals(
            byteArrayOf(
                7, 6, 5, 4, 3, 2, 1, 0,
                17, 16, 15, 14, 13, 12, 11, 10,
                27, 26, 25, 24, 23, 22, 21, 20,
                37, 36, 35, 34, 33, 32, 31, 30,
            ),
            result84.y,
        )
        // Chroma dims: 4x2
        assertArrayEquals(
            byteArrayOf(43, 42, 41, 40, 53, 52, 51, 50),
            result84.u,
        )
        assertArrayEquals(
            byteArrayOf(83, 82, 81, 80, 93, 92, 91, 90),
            result84.v,
        )
    }

    @Test
    fun `flipHorizontallyInPlace double-flip round-trips to identical original byte arrays`() {
        val f = frame(8, 4, ts = 555L)
        val origY = f.y.clone()
        val origU = f.u.clone()
        val origV = f.v.clone()

        val first = FrameConverter.flipHorizontallyInPlace(f)
        assertSame(f, first)

        val second = FrameConverter.flipHorizontallyInPlace(first)
        assertSame(f, second)
        assertEquals(8, second.width)
        assertEquals(4, second.height)
        assertEquals(555L, second.timestampUs)
        assertArrayEquals(origY, second.y)
        assertArrayEquals(origU, second.u)
        assertArrayEquals(origV, second.v)
    }

    @Test
    fun `flipHorizontallyInPlace preserves timestamp and returns same instance`() {
        val f = frame(6, 4, ts = 9876543210L)
        val flipped = FrameConverter.flipHorizontallyInPlace(f)
        assertSame(f, flipped)
        assertEquals(9876543210L, flipped.timestampUs)
        assertEquals(6, flipped.width)
        assertEquals(4, flipped.height)
    }

    @Test
    fun `rotate 90 followed by flipHorizontallyInPlace transforms frame correctly`() {
        // 4x4 so chroma is 2-wide; a 4x2 frame would leave 1-wide chroma where H-flip is a no-op.
        val f = frame(4, 4, ts = 12345L)
        val rotated = FrameConverter.rotate(f, 90)
        assertEquals(4, rotated.width)
        assertEquals(4, rotated.height)

        val flipped = FrameConverter.flipHorizontallyInPlace(rotated)
        assertSame(rotated, flipped)
        assertEquals(4, flipped.width)
        assertEquals(4, flipped.height)
        assertEquals(12345L, flipped.timestampUs)

        // After 90 CW + horizontal flip:
        //  0  10 20 30
        //  1  11 21 31
        //  2  12 22 32
        //  3  13 23 33
        assertArrayEquals(
            byteArrayOf(0, 10, 20, 30, 1, 11, 21, 31, 2, 12, 22, 32, 3, 13, 23, 33),
            flipped.y,
        )
        // Chroma 2x2: 90 CW then H-flip is not the original and not a chroma no-op.
        assertArrayEquals(byteArrayOf(40, 50, 41, 51), flipped.u)
        assertArrayEquals(byteArrayOf(80, 90, 81, 91), flipped.v)
    }

    @Test
    fun `rotate 180 followed by flipHorizontallyInPlace transforms frame correctly`() {
        val f = frame(4, 4, ts = 54321L)
        val rotated = FrameConverter.rotate(f, 180)
        assertEquals(4, rotated.width)
        assertEquals(4, rotated.height)

        val flipped = FrameConverter.flipHorizontallyInPlace(rotated)
        assertSame(rotated, flipped)
        assertEquals(4, flipped.width)
        assertEquals(4, flipped.height)
        assertEquals(54321L, flipped.timestampUs)

        // 180 then H-flip is a vertical flip of the original 4x4.
        assertArrayEquals(
            byteArrayOf(30, 31, 32, 33, 20, 21, 22, 23, 10, 11, 12, 13, 0, 1, 2, 3),
            flipped.y,
        )
        assertArrayEquals(byteArrayOf(50, 51, 40, 41), flipped.u)
        assertArrayEquals(byteArrayOf(90, 91, 80, 81), flipped.v)
    }

    @Test
    fun `rotate 270 followed by flipHorizontallyInPlace transforms frame correctly`() {
        val f = frame(4, 4, ts = 67890L)
        val rotated = FrameConverter.rotate(f, 270)
        assertEquals(4, rotated.width)
        assertEquals(4, rotated.height)

        val flipped = FrameConverter.flipHorizontallyInPlace(rotated)
        assertSame(rotated, flipped)
        assertEquals(4, flipped.width)
        assertEquals(4, flipped.height)
        assertEquals(67890L, flipped.timestampUs)

        // After 270 CW + horizontal flip:
        //  33 23 13 3
        //  32 22 12 2
        //  31 21 11 1
        //  30 20 10 0
        assertArrayEquals(
            byteArrayOf(33, 23, 13, 3, 32, 22, 12, 2, 31, 21, 11, 1, 30, 20, 10, 0),
            flipped.y,
        )
        assertArrayEquals(byteArrayOf(51, 41, 50, 40), flipped.u)
        assertArrayEquals(byteArrayOf(91, 81, 90, 80), flipped.v)
    }

    @Test
    fun `flipHorizontallyInPlace handles minimal 2x2 frame and 0x0 frame safely`() {
        val f22 = frame(2, 2, ts = 1L)
        val flipped22 = FrameConverter.flipHorizontallyInPlace(f22)
        assertSame(f22, flipped22)
        assertArrayEquals(byteArrayOf(1, 0, 11, 10), flipped22.y)
        assertArrayEquals(byteArrayOf(40), flipped22.u)
        assertArrayEquals(byteArrayOf(80), flipped22.v)

        val empty = FrameData(
            width = 0,
            height = 0,
            y = ByteArray(0),
            u = ByteArray(0),
            v = ByteArray(0),
            timestampUs = 0L,
        )
        val flippedEmpty = FrameConverter.flipHorizontallyInPlace(empty)
        assertSame(empty, flippedEmpty)
        assertEquals(0, flippedEmpty.y.size)
    }

    @Test
    fun `flipHorizontallyInPlace operates in sub-millisecond time on 1080p frame`() {
        val width = 1920
        val height = 1080
        val cw = width / 2
        val ch = height / 2
        fun patterned(size: Int, mul: Int) = ByteArray(size) { i -> ((i * mul) % 251).toByte() }
        val f1080 = FrameData(
            width = width,
            height = height,
            y = patterned(width * height, 1),
            u = patterned(cw * ch, 3),
            v = patterned(cw * ch, 7),
            timestampUs = 123456L,
        )
        val origY0 = f1080.y[0]
        val origYRight = f1080.y[width - 1]
        val origYLastLeft = f1080.y[(height - 1) * width]
        val origU0 = f1080.u[0]
        val origURight = f1080.u[cw - 1]
        val origVLastLeft = f1080.v[(ch - 1) * cw]
        val origVLastRight = f1080.v[(ch - 1) * cw + (cw - 1)]

        val warmup = FrameData(
            width = width,
            height = height,
            y = ByteArray(width * height),
            u = ByteArray(cw * ch),
            v = ByteArray(cw * ch),
            timestampUs = 0L,
        )
        repeat(5) { FrameConverter.flipHorizontallyInPlace(warmup) }

        val start = System.nanoTime()
        FrameConverter.flipHorizontallyInPlace(f1080)
        val elapsedMs = (System.nanoTime() - start) / 1_000_000.0

        assertEquals(origYRight, f1080.y[0])
        assertEquals(origY0, f1080.y[width - 1])
        assertEquals(origYLastLeft, f1080.y[(height - 1) * width + (width - 1)])
        assertEquals(origURight, f1080.u[0])
        assertEquals(origU0, f1080.u[cw - 1])
        assertEquals(origVLastRight, f1080.v[(ch - 1) * cw])
        assertEquals(origVLastLeft, f1080.v[(ch - 1) * cw + (cw - 1)])
        // One 30fps frame is 33ms; a no-op would pass a loose bound without the U/V asserts.
        org.junit.Assert.assertTrue(
            "Elapsed time $elapsedMs ms should be under the 30fps budget (33ms)",
            elapsedMs < 33.0,
        )
    }
}
