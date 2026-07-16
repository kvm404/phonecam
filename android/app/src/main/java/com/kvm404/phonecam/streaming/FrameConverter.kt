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
    @Suppress("LongParameterList")
    fun copyPlane(
        src: ByteBuffer,
        width: Int,
        height: Int,
        rowStride: Int,
        pixelStride: Int,
        rowOffset: Int = 0,
        colOffset: Int = 0,
    ): ByteArray {
        val out = ByteArray(width * height)
        var outPos = 0
        for (row in 0 until height) {
            val rowStart = (row + rowOffset) * rowStride
            if (pixelStride == 1) {
                for (col in 0 until width) {
                    out[outPos++] = src.get(rowStart + colOffset + col)
                }
            } else {
                for (col in 0 until width) {
                    out[outPos++] = src.get(rowStart + (colOffset + col) * pixelStride)
                }
            }
        }
        return out
    }

    /**
     * Build a [FrameData] from the three YUV_420_888 plane buffers and their strides. The
     * chroma planes are half width/height. Pure: no Android types, fully JVM-testable.
     *
     * When [targetWidth]/[targetHeight] are set (non-zero) and the source frame is larger,
     * the frame is center-cropped to exactly that size — cameras often negotiate a wider
     * stream (e.g. 1600x720 on 20:9 sensors) than the encoder was configured for, and
     * feeding oversized planes into the codec input image overflows its buffers. Offsets
     * are kept even so the chroma planes stay aligned. A source smaller than the target in
     * either dimension is an error: cropping cannot invent pixels.
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
        targetWidth: Int = 0,
        targetHeight: Int = 0,
    ): FrameData {
        val outWidth = if (targetWidth > 0) targetWidth else width
        val outHeight = if (targetHeight > 0) targetHeight else height
        require(width >= outWidth && height >= outHeight) {
            "camera frame ${width}x$height is smaller than encoder target ${outWidth}x$outHeight"
        }
        val top = ((height - outHeight) / 2) and 1.inv()
        val left = ((width - outWidth) / 2) and 1.inv()

        val chromaWidth = outWidth / 2
        val chromaHeight = outHeight / 2
        return FrameData(
            width = outWidth,
            height = outHeight,
            y = copyPlane(yBuffer, outWidth, outHeight, yRowStride, yPixelStride, top, left),
            u = copyPlane(uBuffer, chromaWidth, chromaHeight, uRowStride, uPixelStride, top / 2, left / 2),
            v = copyPlane(vBuffer, chromaWidth, chromaHeight, vRowStride, vPixelStride, top / 2, left / 2),
            timestampUs = timestampUs,
        )
    }
}
