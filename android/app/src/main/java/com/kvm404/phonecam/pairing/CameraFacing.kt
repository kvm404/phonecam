package com.kvm404.phonecam.pairing

/**
 * Last-used lens facing persisted in SharedPreferences `"phonecam"` / [PREF_KEY].
 *
 * Flip is default back/front only. The QR payload never carries this; `/pair` and
 * `/reconnect` report it so `/status` can echo the facing from the first handshake.
 */
object CameraFacing {
    const val PREF_KEY = "camera_facing"
    const val BACK = "back"
    const val FRONT = "front"

    /** Unknown / missing prefs (fresh install, corrupted value) default to back. */
    fun fromPref(value: String?): String = if (value == FRONT) FRONT else BACK

    /**
     * True when both default back and front facings are present.
     *
     * [lensFacings] are CameraSelector.LENS_FACING_* ints. Extra back lenses and
     * EXTERNAL cameras do not count — flip does not cycle them.
     */
    fun canFlip(
        lensFacings: Iterable<Int>,
        back: Int,
        front: Int,
    ): Boolean {
        var hasBack = false
        var hasFront = false
        for (facing in lensFacings) {
            if (facing == back) hasBack = true
            if (facing == front) hasFront = true
        }
        return hasBack && hasFront
    }
}
