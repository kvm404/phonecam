package com.kvm404.phonecam.pairing

/**
 * User-selectable capture/encode quality preset.
 *
 * The selected preset is the canonical resolution for a whole streaming session: it sizes the
 * camera [android.util.Size] ImageAnalysis target (the dominant per-frame CPU cost on weak
 * devices — crop -> rotate -> compose -> NV12 pack all run at this resolution), the fixed
 * composition canvas, the encoder profile, and the dims announced to the laptop at `/pair`.
 *
 * All presets are 16:9 landscape at 30 fps. Lowering the resolution is the fix for weak SoCs
 * (measured ~3 fps at 720p on a Samsung A03 / Unisoc SC9863A); [HIGH] keeps capable phones
 * (the reference vivo) at 720p, so it is the default and leaves prior behaviour unchanged.
 *
 * Pure / JVM-testable: no Android framework imports.
 */
enum class StreamQuality(
    val width: Int,
    val height: Int,
    val fps: Int,
    /** Stable key persisted in SharedPreferences; never localise or reorder. */
    val key: String,
    /** Human-readable label for the Home selector. */
    val label: String,
) {
    HIGH(1280, 720, 30, "high", "High (720p)"),
    MEDIUM(960, 540, 30, "medium", "Medium (540p)"),
    LOW(640, 360, 30, "low", "Low (360p)");

    /** The [VideoProfile] this preset commits to (canvas / encoder / `/pair` dims). */
    fun toProfile(): VideoProfile = VideoProfile(width = width, height = height, fps = fps)

    companion object {
        /** Reference-device default; keeps the vivo/720p behaviour unchanged. */
        val DEFAULT: StreamQuality = HIGH

        /**
         * Resolve a persisted key back to a preset. Unknown / null keys (missing pref, older
         * install, corrupted value) fall back to [DEFAULT].
         */
        fun fromKey(key: String?): StreamQuality =
            entries.firstOrNull { it.key == key } ?: DEFAULT
    }
}
