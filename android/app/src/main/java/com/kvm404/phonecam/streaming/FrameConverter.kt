package com.kvm404.phonecam.streaming

import java.nio.ByteBuffer

/**
 * A single camera frame as tightly-packed I420 (planar YUV 4:2:0) planes plus a capture
 * timestamp. This is a plain data holder with no Android types, so the encoder input path
 * and the conversion core can both be tested on the JVM.
 */
class FrameData(
    val width: Int,
    val height: Int,
    /** Luma plane, width*height bytes, tightly packed. */
    val y: ByteArray,
    /** Cb plane, (width/2)*(height/2) bytes, tightly packed. */
    val u: ByteArray,
    /** Cr plane, (width/2)*(height/2) bytes, tightly packed. */
    val v: ByteArray,
    val timestampUs: Long,
)

/**
 * Converts CameraX `YUV_420_888` planes into a tightly-packed [FrameData]. The plane-copy
 * core operates purely on [ByteBuffer]s and stride integers so it can be JVM-tested with
 * synthetic buffers; the ImageProxy-touching glue stays in the caller (MainActivity).
 */
object FrameConverter {

    /**
     * Copy one plane into a tightly-packed `width*height` array, honouring `rowStride`
     * (padding at the end of each row) and `pixelStride` (1 for planar, 2 for the
     * interleaved chroma that many devices produce). Uses absolute gets so the source
     * buffer's position is left untouched.
     */
    fun copyPlane(
        src: ByteBuffer,
        width: Int,
        height: Int,
        rowStride: Int,
        pixelStride: Int,
    ): ByteArray {
        val out = ByteArray(width * height)
        var outPos = 0
        for (row in 0 until height) {
            val rowStart = row * rowStride
            if (pixelStride == 1) {
                for (col in 0 until width) {
                    out[outPos++] = src.get(rowStart + col)
                }
            } else {
                for (col in 0 until width) {
                    out[outPos++] = src.get(rowStart + col * pixelStride)
                }
            }
        }
        return out
    }

    /**
     * Build a [FrameData] from the three YUV_420_888 plane buffers and their strides. The
     * chroma planes are half width/height. Pure: no Android types, fully JVM-testable.
     */
    @Suppress("LongParameterList")
    fun toFrameData(
        width: Int,
        height: Int,
        yBuffer: ByteBuffer,
        yRowStride: Int,
        yPixelStride: Int,
        uBuffer: ByteBuffer,
        uRowStride: Int,
        uPixelStride: Int,
        vBuffer: ByteBuffer,
        vRowStride: Int,
        vPixelStride: Int,
        timestampUs: Long,
    ): FrameData {
        val chromaWidth = width / 2
        val chromaHeight = height / 2
        return FrameData(
            width = width,
            height = height,
            y = copyPlane(yBuffer, width, height, yRowStride, yPixelStride),
            u = copyPlane(uBuffer, chromaWidth, chromaHeight, uRowStride, uPixelStride),
            v = copyPlane(vBuffer, chromaWidth, chromaHeight, vRowStride, vPixelStride),
            timestampUs = timestampUs,
        )
    }
}
