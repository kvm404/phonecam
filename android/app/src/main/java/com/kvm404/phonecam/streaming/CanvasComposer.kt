package com.kvm404.phonecam.streaming

/**
 * Composes an already-upright [FrameData] onto a fixed landscape canvas of exactly
 * [canvasWidth]x[canvasHeight]. This is the "dynamic content on a fixed canvas" core of the
 * gmeet-like orientation handling: the encoder and `/pair` dims never change, so every frame —
 * landscape or portrait — must be normalized back to the canvas here.
 *
 *  - Landscape content already matching the canvas is returned untouched (the hot path; no
 *    allocation or copy).
 *  - Portrait (or any smaller) content is scaled-to-fit preserving aspect ratio (only downscale
 *    cases arise from the crop→rotate pipeline) and centered on a black canvas with pillarbox
 *    bars. Black is I420 studio black: Y=16, U=V=128.
 *
 * Pure and Android-free (operates on tightly-packed I420 [FrameData] only) so it is JVM-tested.
 * Scaling is integer nearest-neighbor; scaled dims and offsets are forced even so the half-res
 * chroma planes stay aligned.
 */
object CanvasComposer {

    /** I420 studio-black luma. */
    private const val BLACK_Y: Byte = 16

    /** Neutral chroma (128 unsigned == -128 as a signed byte). */
    private val NEUTRAL_CHROMA: Byte = 128.toByte()

    /**
     * Return [frame] composed onto a [canvasWidth]x[canvasHeight] black canvas. When the frame
     * already fills the canvas the same instance is returned (no copy). Otherwise the frame is
     * scaled to fit and centered, producing a new canvas-sized [FrameData].
     */
    fun compose(frame: FrameData, canvasWidth: Int, canvasHeight: Int): FrameData {
        if (frame.width == canvasWidth && frame.height == canvasHeight) return frame

        // Scale-to-fit preserving aspect: pick the limiting dimension, then force the other to
        // even. Using Long products avoids overflow at real frame sizes.
        val scaledWidth: Int
        val scaledHeight: Int
        if (canvasWidth.toLong() * frame.height >= canvasHeight.toLong() * frame.width) {
            // Height is the limiting dimension (e.g. portrait onto landscape).
            scaledHeight = canvasHeight
            scaledWidth = ((frame.width.toLong() * canvasHeight / frame.height).toInt()) and 1.inv()
        } else {
            scaledWidth = canvasWidth
            scaledHeight = ((frame.height.toLong() * canvasWidth / frame.width).toInt()) and 1.inv()
        }

        // Even offsets so chroma (half res) lands on integer positions.
        val xOffset = ((canvasWidth - scaledWidth) / 2) and 1.inv()
        val yOffset = ((canvasHeight - scaledHeight) / 2) and 1.inv()

        val y = ByteArray(canvasWidth * canvasHeight).also { it.fill(BLACK_Y) }
        val chromaW = canvasWidth / 2
        val chromaH = canvasHeight / 2
        val u = ByteArray(chromaW * chromaH).also { it.fill(NEUTRAL_CHROMA) }
        val v = ByteArray(chromaW * chromaH).also { it.fill(NEUTRAL_CHROMA) }

        scalePlane(
            src = frame.y, srcW = frame.width, srcH = frame.height,
            dst = y, dstStride = canvasWidth, dstX = xOffset, dstY = yOffset,
            scaledW = scaledWidth, scaledH = scaledHeight,
        )
        scalePlane(
            src = frame.u, srcW = frame.width / 2, srcH = frame.height / 2,
            dst = u, dstStride = chromaW, dstX = xOffset / 2, dstY = yOffset / 2,
            scaledW = scaledWidth / 2, scaledH = scaledHeight / 2,
        )
        scalePlane(
            src = frame.v, srcW = frame.width / 2, srcH = frame.height / 2,
            dst = v, dstStride = chromaW, dstX = xOffset / 2, dstY = yOffset / 2,
            scaledW = scaledWidth / 2, scaledH = scaledHeight / 2,
        )

        return FrameData(canvasWidth, canvasHeight, y, u, v, frame.timestampUs)
    }

    /**
     * Nearest-neighbor copy of the [srcW]x[srcH] [src] plane into the [scaledW]x[scaledH]
     * rectangle of [dst] (row stride [dstStride]) at ([dstX], [dstY]). Integer math only.
     */
    @Suppress("LongParameterList")
    private fun scalePlane(
        src: ByteArray,
        srcW: Int,
        srcH: Int,
        dst: ByteArray,
        dstStride: Int,
        dstX: Int,
        dstY: Int,
        scaledW: Int,
        scaledH: Int,
    ) {
        for (row in 0 until scaledH) {
            val srcRowBase = (row * srcH / scaledH) * srcW
            val dstRowBase = (dstY + row) * dstStride + dstX
            for (col in 0 until scaledW) {
                dst[dstRowBase + col] = src[srcRowBase + (col * srcW / scaledW)]
            }
        }
    }
}
