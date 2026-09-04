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
    fun copyPlaneInto(
        src: ByteBuffer,
        width: Int,
        height: Int,
        rowStride: Int,
        pixelStride: Int,
        srcRowOffset: Int,
        srcColOffset: Int,
        dst: ByteArray,
        dstStride: Int,
        dstRowOffset: Int = 0,
        dstColOffset: Int = 0,
    ) {
        val base = src.position()
        for (row in 0 until height) {
            val rowStart = (row + srcRowOffset) * rowStride
            var dstPos = (row + dstRowOffset) * dstStride + dstColOffset
            if (pixelStride == 1) {
                for (col in 0 until width) {
                    dst[dstPos++] = src.get(base + rowStart + srcColOffset + col)
                }
            } else {
                for (col in 0 until width) {
                    dst[dstPos++] = src.get(base + rowStart + (srcColOffset + col) * pixelStride)
                }
            }
        }
    }

    /**
     * Copy one plane into a tightly-packed `width*height` array, honouring `rowStride`
     * (padding at the end of each row) and `pixelStride` (1 for planar, 2 for the
     * interleaved chroma that many devices produce). Uses absolute gets offset by `src.position()`
     * so the source buffer's position is left untouched while respecting slice offsets.
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
        copyPlaneInto(
            src, width, height, rowStride, pixelStride,
            rowOffset, colOffset, out, width, 0, 0,
        )
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
     * are kept even so the chroma planes stay aligned. When a source frame is smaller than
     * the target, it is centered on a black canvas (pillarbox/letterbox) to avoid crashing.
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
        val cropW = minOf(width, outWidth) and 1.inv()
        val cropH = minOf(height, outHeight) and 1.inv()
        val srcTop = ((height - cropH) / 2) and 1.inv()
        val srcLeft = ((width - cropW) / 2) and 1.inv()
        val dstTop = ((outHeight - cropH) / 2) and 1.inv()
        val dstLeft = ((outWidth - cropW) / 2) and 1.inv()

        val y = ByteArray(outWidth * outHeight).also { it.fill(16.toByte()) }
        val chromaWidth = outWidth / 2
        val chromaHeight = outHeight / 2
        val u = ByteArray(chromaWidth * chromaHeight).also { it.fill(128.toByte()) }
        val v = ByteArray(chromaWidth * chromaHeight).also { it.fill(128.toByte()) }

        copyPlaneInto(
            yBuffer, cropW, cropH, yRowStride, yPixelStride,
            srcTop, srcLeft, y, outWidth, dstTop, dstLeft,
        )
        copyPlaneInto(
            uBuffer, cropW / 2, cropH / 2, uRowStride, uPixelStride,
            srcTop / 2, srcLeft / 2, u, chromaWidth, dstTop / 2, dstLeft / 2,
        )
        copyPlaneInto(
            vBuffer, cropW / 2, cropH / 2, vRowStride, vPixelStride,
            srcTop / 2, srcLeft / 2, v, chromaWidth, dstTop / 2, dstLeft / 2,
        )

        return FrameData(
            width = outWidth,
            height = outHeight,
            y = y,
            u = u,
            v = v,
            timestampUs = timestampUs,
        )
    }

    /**
     * Rotate a tightly-packed I420 [frame] by [degrees] (0, 90, 180, or 270) so the encoded
     * image is upright. The semantics match `ImageProxy.imageInfo.rotationDegrees`: [degrees]
     * is the clockwise rotation to APPLY to the buffer to make it display-correct.
     *
     * All three planes are rotated (chroma at half resolution); 90/270 swap width and height.
     * The timestamp is preserved. 0 is a no-op that returns the same instance.
     *
     * Index math, in (row, col) terms with source dims (h, w):
     *  - 90°  CW: out[r][c] = src[h-1-c][r]   (out dims w×h -> rows=w, cols=h)
     *  - 180°   : out[r][c] = src[h-1-r][w-1-c]
     *  - 270° CW: out[r][c] = src[c][w-1-r]   (out dims w×h -> rows=w, cols=h)
     */
    fun rotate(frame: FrameData, degrees: Int): FrameData {
        return when (degrees) {
            0 -> frame
            90, 180, 270 -> {
                val (newWidth, newHeight) =
                    if (degrees == 180) frame.width to frame.height
                    else frame.height to frame.width
                FrameData(
                    width = newWidth,
                    height = newHeight,
                    y = rotatePlane(frame.y, frame.width, frame.height, degrees),
                    u = rotatePlane(frame.u, frame.width / 2, frame.height / 2, degrees),
                    v = rotatePlane(frame.v, frame.width / 2, frame.height / 2, degrees),
                    timestampUs = frame.timestampUs,
                )
            }
            else -> throw IllegalArgumentException(
                "unsupported rotation: $degrees (expected 0, 90, 180, or 270)"
            )
        }
    }

    /**
     * Rotate one tightly-packed plane of dimensions [srcW]x[srcH] by [degrees]. For 90/270
     * the output is [srcH]x[srcW]; for 180 the dimensions are unchanged.
     */
    private fun rotatePlane(src: ByteArray, srcW: Int, srcH: Int, degrees: Int): ByteArray {
        val out = ByteArray(src.size)
        when (degrees) {
            90 -> {
                // out dims: rows = srcW, cols = srcH (dstWidth = srcH).
                for (r in 0 until srcW) {
                    for (c in 0 until srcH) {
                        out[r * srcH + c] = src[(srcH - 1 - c) * srcW + r]
                    }
                }
            }
            180 -> {
                for (r in 0 until srcH) {
                    for (c in 0 until srcW) {
                        out[r * srcW + c] = src[(srcH - 1 - r) * srcW + (srcW - 1 - c)]
                    }
                }
            }
            270 -> {
                // out dims: rows = srcW, cols = srcH (dstWidth = srcH).
                for (r in 0 until srcW) {
                    for (c in 0 until srcH) {
                        out[r * srcH + c] = src[c * srcW + (srcW - 1 - r)]
                    }
                }
            }
        }
        return out
    }

    /**
     * Horizontally flip a tightly-packed I420 [frame] in place.
     * Reverses each row of Y, U, and V planes using two pointers without any memory allocation.
     * Returns the same [frame] instance.
     */
    fun flipHorizontallyInPlace(frame: FrameData): FrameData {
        flipPlaneHorizontallyInPlace(frame.y, frame.width, frame.height)
        val chromaWidth = frame.width / 2
        val chromaHeight = frame.height / 2
        flipPlaneHorizontallyInPlace(frame.u, chromaWidth, chromaHeight)
        flipPlaneHorizontallyInPlace(frame.v, chromaWidth, chromaHeight)
        return frame
    }

    private fun flipPlaneHorizontallyInPlace(plane: ByteArray, width: Int, height: Int) {
        for (row in 0 until height) {
            var left = row * width
            var right = left + width - 1
            while (left < right) {
                val tmp = plane[left]
                plane[left] = plane[right]
                plane[right] = tmp
                left++
                right--
            }
        }
    }
}
