package com.kvm404.phonecam.streaming

import com.kvm404.phonecam.pairing.CameraFacing
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * JVM coverage for persist/load/toggle, per-lens keys, bind/fallback reload, and
 * releaseRam leaving prefs intact.
 */
class CameraMirrorTest {

    private val prefs = mutableMapOf<String, Boolean>()
    private val store = CameraMirror(
        getBoolean = { key, default -> prefs[key] ?: default },
        putBoolean = { key, value -> prefs[key] = value },
    )

    @Test
    fun `pref key is camera_mirror plus cameraLabel`() {
        assertEquals("camera_mirror_back", CameraMirror.prefKey(CameraFacing.BACK))
        assertEquals("camera_mirror_front", CameraMirror.prefKey(CameraFacing.FRONT))
    }

    @Test
    fun `loadMirrorPreference defaults false when the lens key is missing`() {
        assertFalse(store.loadMirrorPreference(CameraFacing.BACK))
        assertFalse(store.isMirrored)
        assertTrue(prefs.isEmpty())
        assertFalse(store.loadMirrorPreference(CameraFacing.FRONT))
    }

    @Test
    fun `toggleMirror persists only the current lens`() {
        assertTrue(store.toggleMirror(CameraFacing.BACK))
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.BACK)])
        assertFalse(prefs.containsKey(CameraMirror.prefKey(CameraFacing.FRONT)))

        assertFalse(store.loadMirrorPreference(CameraFacing.FRONT))
        assertTrue(store.toggleMirror(CameraFacing.FRONT))
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.BACK)])
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.FRONT)])
    }

    @Test
    fun `bind reload uses the current facing not the other lens`() {
        store.toggleMirror(CameraFacing.FRONT)
        // Bind/rebind the back camera: must not leak the front's true.
        assertFalse(store.loadMirrorPreference(CameraFacing.BACK))
        assertFalse(store.isMirrored)
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.FRONT)])
    }

    @Test
    fun `fallback reload after a facing change loads the fallback lens`() {
        store.toggleMirror(CameraFacing.FRONT)
        // First-bind asked for front, then fell back to back.
        assertFalse(store.loadMirrorPreference(CameraFacing.BACK))
        assertFalse(store.isMirrored)

        store.toggleMirror(CameraFacing.BACK)
        // Flip fallback: requested back, stay on front — reload front's true.
        assertTrue(store.loadMirrorPreference(CameraFacing.FRONT))
        assertTrue(store.isMirrored)
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.BACK)])
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.FRONT)])
    }

    @Test
    fun `releaseRam clears the in-memory flag and keeps per-lens prefs`() {
        store.toggleMirror(CameraFacing.BACK)
        store.loadMirrorPreference(CameraFacing.FRONT)
        store.toggleMirror(CameraFacing.FRONT)
        store.toggleMirror(CameraFacing.FRONT) // front stored as false
        store.loadMirrorPreference(CameraFacing.BACK)
        assertTrue(store.isMirrored)

        store.releaseRam()
        assertFalse(store.isMirrored)
        assertEquals(true, prefs[CameraMirror.prefKey(CameraFacing.BACK)])
        assertEquals(false, prefs[CameraMirror.prefKey(CameraFacing.FRONT)])

        assertTrue(store.loadMirrorPreference(CameraFacing.BACK))
        assertFalse(store.loadMirrorPreference(CameraFacing.FRONT))
    }
}
