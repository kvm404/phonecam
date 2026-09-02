package com.kvm404.phonecam.streaming

import kotlin.math.roundToInt

/**
 * Pure zoom math for the LIVE-screen zoom controls: the 0.25x step/clamp rules, the
 * reset-to-1x target, the readout formatting ("1x", "1.25x", "1.5x", "2x" — no trailing
 * ".0"), and the hide predicate for lenses without a zoom range.
 *
 * No Android imports, so JVM unit tests cover it directly;
 * [com.kvm404.phonecam.StreamingService] feeds it the live values from the bound camera's
 * [androidx.camera.core.ZoomState].
 */
object ZoomStepper {

    /** Ratio change per press. A multiple of 0.25 so every reachable ratio formats cleanly. */
    const val STEP = 0.25f

    /** Reset target before clamping: 1x is the lens' native field of view. */
    const val RESET_RATIO = 1.0f

    /** Slack for float compares against camera-reported ratios. */
    private const val EPSILON = 1e-4f

    /** One step in, clamped to [maxRatio] (front lenses often stop just past 1x). */
    fun stepUp(currentRatio: Float, maxRatio: Float): Float =
        (currentRatio + STEP).coerceAtMost(maxRatio)

    /** One step out, clamped to [minRatio] (ultra-wide lenses start below 1x). */
    fun stepDown(currentRatio: Float, minRatio: Float): Float =
        (currentRatio - STEP).coerceAtLeast(minRatio)

    /** Reset-to-1x target, clamped into the camera's reported range. */
    fun resetTarget(minRatio: Float, maxRatio: Float): Float =
        RESET_RATIO.coerceAtLeast(minRatio).coerceAtMost(maxRatio)

    /** True while stepping out still has range left (zoom-out button enabled state). */
    fun canZoomOut(currentRatio: Float, minRatio: Float): Boolean =
        currentRatio > minRatio + EPSILON

    /** True while stepping in still has range left (zoom-in button enabled state). */
    fun canZoomIn(currentRatio: Float, maxRatio: Float): Boolean =
        currentRatio < maxRatio - EPSILON

    /** True when reset would actually change the ratio, in either direction (reset enabled). */
    fun canReset(currentRatio: Float, minRatio: Float, maxRatio: Float): Boolean {
        val target = resetTarget(minRatio, maxRatio)
        return currentRatio < target - EPSILON || currentRatio > target + EPSILON
    }

    /** Hide the whole zoom row when the active lens cannot zoom (maxZoomRatio <= 1.0). */
    fun shouldShow(maxZoomRatio: Float): Boolean = maxZoomRatio > RESET_RATIO

    /**
     * Readout without a trailing ".0": 1.0 -> "1x", 1.25 -> "1.25x", 1.5 -> "1.5x",
     * 2.0 -> "2x". Rounds to hundredths first so float noise can never reach the label.
     */
    fun format(ratio: Float): String {
        val hundredths = (ratio * 100f).roundToInt()
        return if (hundredths % 100 == 0) {
            "${hundredths / 100}x"
        } else {
            "${hundredths / 100.0}x"
        }
    }
}
