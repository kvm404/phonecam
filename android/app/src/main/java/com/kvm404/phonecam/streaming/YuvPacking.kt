package com.kvm404.phonecam.streaming

/**
 * Pure, Android-free packing of tightly-packed I420 planes into the contiguous byte layout a
 * MediaCodec input [java.nio.ByteBuffer] expects for a given negotiated color format. Kept
 * Android-type-free so it is exhaustively JVM-testable; [VideoEncoder] calls these and puts the
 * result straight into the codec input buffer.
 *
 * Input is always a [FrameData]'s three planes: `y` (width*height), `u` and `v` each
 * (width/2)*(height/2), planar with no row padding.
 */
object YuvPacking {

    /**
     * I420 / COLOR_FormatYUV420Planar: the three planes copied contiguously as Y | U | V. Since
     * [FrameData] is already exactly this layout, this is a straight three-part copy.
     */
    fun packI420(y: ByteArray, u: ByteArray, v: ByteArray, width: Int, height: Int): ByteArray {
        val ySize = width * height
        val chromaSize = (width / 2) * (height / 2)
        val out = ByteArray(ySize + chromaSize * 2)
        System.arraycopy(y, 0, out, 0, ySize)
        System.arraycopy(u, 0, out, ySize, chromaSize)
        System.arraycopy(v, 0, out, ySize + chromaSize, chromaSize)
        return out
    }

    /**
     * NV12 / COLOR_FormatYUV420SemiPlanar: the Y plane copied verbatim, then the chroma
     * interleaved as U,V,U,V… (Cb first, Cr second).
     */
    fun packNv12(y: ByteArray, u: ByteArray, v: ByteArray, width: Int, height: Int): ByteArray {
        val ySize = width * height
        val chromaSize = (width / 2) * (height / 2)
        val out = ByteArray(ySize + chromaSize * 2)
        System.arraycopy(y, 0, out, 0, ySize)
        var pos = ySize
        for (i in 0 until chromaSize) {
            out[pos++] = u[i]
            out[pos++] = v[i]
        }
        return out
    }
}
