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
}
