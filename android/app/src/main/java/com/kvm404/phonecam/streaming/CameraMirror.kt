package com.kvm404.phonecam.streaming

/**
 * Per-lens stream-mirror flag. Pref keys are [PREF_PREFIX] + cameraLabel (`back`/`front`).
 * Missing keys default to false. [releaseRam] drops only the in-memory flag so a later
 * bind can reload the stored per-facing choice.
 *
 * No Android types: [StreamingService] supplies SharedPreferences get/put, and JVM tests
 * use a map so a cross-lens leak fails CI.
 */
class CameraMirror(
    private val getBoolean: (key: String, default: Boolean) -> Boolean,
    private val putBoolean: (key: String, value: Boolean) -> Unit,
) {
    /** Analyzer-thread read; written on the main thread from toggle/load/release. */
    @Volatile
    var isMirrored: Boolean = false
        private set

    fun loadMirrorPreference(cameraLabel: String): Boolean {
        isMirrored = getBoolean(prefKey(cameraLabel), DEFAULT)
        return isMirrored
    }

    fun persistMirror(cameraLabel: String) {
        putBoolean(prefKey(cameraLabel), isMirrored)
    }

    fun toggleMirror(cameraLabel: String): Boolean {
        isMirrored = !isMirrored
        persistMirror(cameraLabel)
        return isMirrored
    }

    /** Session teardown: RAM only. Per-lens prefs stay. */
    fun releaseRam() {
        isMirrored = false
    }

    companion object {
        const val PREF_PREFIX = "camera_mirror_"
        const val DEFAULT = false

        fun prefKey(cameraLabel: String): String = PREF_PREFIX + cameraLabel
    }
}
