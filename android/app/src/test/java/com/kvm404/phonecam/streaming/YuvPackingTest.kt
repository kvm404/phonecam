package com.kvm404.phonecam.streaming

import org.junit.Assert.assertArrayEquals
import org.junit.Test

/**
 * JVM tests for [YuvPacking]: the pure I420 -> I420 (planar) and I420 -> NV12 (semi-planar)
 * packing that feeds MediaCodec's raw input buffer when the negotiated color format is not the
 * flexible one. Small synthetic frames with distinct plane values so the byte layout is exact.
 */
class YuvPackingTest {

    /** 4x4 luma (16 bytes) with distinct chroma: U = [10,11,12,13], V = [20,21,22,23]. */
    private val y = ByteArray(16) { (it + 1).toByte() } // 1..16
    private val u = byteArrayOf(10, 11, 12, 13)
    private val v = byteArrayOf(20, 21, 22, 23)

    @Test
    fun `packI420 is y then u then v concatenated`() {
        val out = YuvPacking.packI420(y, u, v, 4, 4)

        val expected = ByteArray(24)
        System.arraycopy(y, 0, expected, 0, 16)
        System.arraycopy(u, 0, expected, 16, 4)
        System.arraycopy(v, 0, expected, 20, 4)
        assertArrayEquals(expected, out)
    }

    @Test
    fun `packNv12 is y then interleaved u v`() {
        val out = YuvPacking.packNv12(y, u, v, 4, 4)

        // Y verbatim, then chroma interleaved U,V,U,V...: 10,20, 11,21, 12,22, 13,23.
        val expected = byteArrayOf(
            1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
            10, 20, 11, 21, 12, 22, 13, 23,
        )
        assertArrayEquals(expected, out)
    }

    @Test
    fun `packI420 and packNv12 produce the same total size`() {
        val i420 = YuvPacking.packI420(y, u, v, 4, 4)
        val nv12 = YuvPacking.packNv12(y, u, v, 4, 4)
        // 4*4 + 2 * (2*2) = 24.
        assertArrayEquals(ByteArray(24), ByteArray(i420.size))
        assertArrayEquals(ByteArray(24), ByteArray(nv12.size))
    }

    @Test
    fun `packNv12 hand-checked 2x2 chroma block`() {
        // 4x4 frame -> chroma is 2x2 (4 samples). Hand-check the interleave for a tiny case.
        val yy = ByteArray(16) { 7 }
        val uu = byteArrayOf(1, 2, 3, 4)
        val vv = byteArrayOf(5, 6, 7, 8)

        val out = YuvPacking.packNv12(yy, uu, vv, 4, 4)

        // Chroma tail after the 16 luma bytes: U0,V0,U1,V1,U2,V2,U3,V3 = 1,5, 2,6, 3,7, 4,8.
        val chromaTail = out.copyOfRange(16, 24)
        assertArrayEquals(byteArrayOf(1, 5, 2, 6, 3, 7, 4, 8), chromaTail)
    }
}
