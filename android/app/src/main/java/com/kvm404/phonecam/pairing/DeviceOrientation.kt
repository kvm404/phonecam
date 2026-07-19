package com.kvm404.phonecam.pairing

/**
 * Pure device-orientation helpers for the sensor-driven, fixed-canvas rotation path. Kept
 * Android-free so the band edges and the inverse Surface mapping are JVM-testable.
 *
 * The raw source is an `OrientationEventListener`: it reports the device rotation clockwise
 * from its natural orientation in degrees, or `ORIENTATION_UNKNOWN` (-1) when the reading is
 * indeterminate (e.g. lying flat). UI rotation alone is insufficient — Android will not report
 * reverse-portrait through configuration changes — so the sensor drives the ImageAnalysis
 * targetRotation directly.
 */

/**
 * Quantize a raw [degrees] reading to the nearest of 0/90/180/270 using 45° bands:
 * 315..44 -> 0, 45..134 -> 90, 135..224 -> 180, 225..314 -> 270. A negative value (the
 * `ORIENTATION_UNKNOWN` sentinel) returns [last] so a momentarily indeterminate reading holds
 * the previous orientation instead of snapping to 0.
 */
fun quantizeOrientation(degrees: Int, last: Int): Int {
    if (degrees < 0) return last
    val d = ((degrees % 360) + 360) % 360
    return when {
        d >= 315 || d < 45 -> 0
        d < 135 -> 90
        d < 225 -> 180
        else -> 270
    }
}

/**
 * Map a quantized device orientation (0/90/180/270) to the `ImageAnalysis.targetRotation` that
 * makes `ImageProxy.imageInfo.rotationDegrees` the clockwise rotation needed to bring the
 * landscape sensor buffer upright for that hold — including the reverse orientations.
 *
 * Returns `Surface.ROTATION_*` values (0/1/2/3) via the standard inverse mapping:
 * 0 -> ROTATION_0 (0), 90 -> ROTATION_270 (3), 180 -> ROTATION_180 (2), 270 -> ROTATION_90 (1).
 */
fun deviceOrientationToSurfaceRotation(orientation: Int): Int = when (orientation) {
    90 -> 3 // Surface.ROTATION_270
    180 -> 2 // Surface.ROTATION_180
    270 -> 1 // Surface.ROTATION_90
    else -> 0 // Surface.ROTATION_0
}
