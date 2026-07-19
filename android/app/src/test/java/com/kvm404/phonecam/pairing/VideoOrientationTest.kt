package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * JVM tests for the pure [effectiveVideo] resolver: orientation choice + buffer rotation ->
 * announced dims + rotation-to-apply. Base profile is the laptop's landscape 1280x720@30.
 */
class VideoOrientationTest {

    private val base = VideoProfile(width = 1280, height = 720, fps = 30)

    private fun assertResult(
        actual: EffectiveVideo,
        width: Int,
        height: Int,
        rotation: Int,
    ) {
        assertEquals("width", width, actual.profile.width)
        assertEquals("height", height, actual.profile.height)
        assertEquals("fps", base.fps, actual.profile.fps)
        assertEquals("rotation", rotation, actual.rotationDegrees)
    }

    // --- AUTO: reproduces the original rotation-degrees behaviour ---

    @Test
    fun `auto with rotation 0 keeps landscape dims and no rotation`() {
        assertResult(effectiveVideo(base, OrientationMode.AUTO, 0), 1280, 720, 0)
    }

    @Test
    fun `auto with rotation 90 swaps to portrait dims and rotates 90`() {
        assertResult(effectiveVideo(base, OrientationMode.AUTO, 90), 720, 1280, 90)
    }

    @Test
    fun `auto with rotation 180 keeps landscape dims and rotates 180`() {
        assertResult(effectiveVideo(base, OrientationMode.AUTO, 180), 1280, 720, 180)
    }

    @Test
    fun `auto with rotation 270 swaps to portrait dims and rotates 270`() {
        assertResult(effectiveVideo(base, OrientationMode.AUTO, 270), 720, 1280, 270)
    }

    // --- PORTRAIT: always portrait dims, rotate landscape buffer 90/270 ---

    @Test
    fun `portrait with rotation 0 forces portrait dims and defaults rotation 90`() {
        assertResult(effectiveVideo(base, OrientationMode.PORTRAIT, 0), 720, 1280, 90)
    }

    @Test
    fun `portrait with rotation 90 forces portrait dims and rotates 90`() {
        assertResult(effectiveVideo(base, OrientationMode.PORTRAIT, 90), 720, 1280, 90)
    }

    @Test
    fun `portrait with rotation 180 forces portrait dims and defaults rotation 90`() {
        assertResult(effectiveVideo(base, OrientationMode.PORTRAIT, 180), 720, 1280, 90)
    }

    @Test
    fun `portrait with rotation 270 forces portrait dims and rotates 270`() {
        assertResult(effectiveVideo(base, OrientationMode.PORTRAIT, 270), 720, 1280, 270)
    }

    // --- LANDSCAPE: always landscape dims, rotate landscape buffer only 0/180 ---

    @Test
    fun `landscape with rotation 0 forces landscape dims and no rotation`() {
        assertResult(effectiveVideo(base, OrientationMode.LANDSCAPE, 0), 1280, 720, 0)
    }

    @Test
    fun `landscape with rotation 90 forces landscape dims and no rotation`() {
        assertResult(effectiveVideo(base, OrientationMode.LANDSCAPE, 90), 1280, 720, 0)
    }

    @Test
    fun `landscape with rotation 180 forces landscape dims and rotates 180`() {
        assertResult(effectiveVideo(base, OrientationMode.LANDSCAPE, 180), 1280, 720, 180)
    }

    @Test
    fun `landscape with rotation 270 forces landscape dims and no rotation`() {
        assertResult(effectiveVideo(base, OrientationMode.LANDSCAPE, 270), 1280, 720, 0)
    }

    // --- fps is always carried through unchanged ---

    @Test
    fun `fps is preserved across all modes`() {
        val profile = VideoProfile(width = 1920, height = 1080, fps = 24)
        assertEquals(24, effectiveVideo(profile, OrientationMode.AUTO, 90).profile.fps)
        assertEquals(24, effectiveVideo(profile, OrientationMode.PORTRAIT, 0).profile.fps)
        assertEquals(24, effectiveVideo(profile, OrientationMode.LANDSCAPE, 0).profile.fps)
    }

    // --- frameRotation: mid-stream rotation against a committed profile ---

    // Committed portrait (scanned holding the phone upright: rotation 90 -> 720x1280@90).
    private val portraitCommitted = effectiveVideo(base, OrientationMode.AUTO, 90)

    // Committed landscape (scanned holding the phone sideways: rotation 0 -> 1280x720@0).
    private val landscapeCommitted = effectiveVideo(base, OrientationMode.AUTO, 0)

    @Test
    fun `frameRotation portrait committed same rotation keeps current`() {
        assertEquals(90, frameRotation(base, portraitCommitted, 90))
    }

    @Test
    fun `frameRotation portrait committed 180 flip uses current rotation`() {
        // 90 -> 270 is a 180° flip; dims stay portrait, so track the flip to stay upright.
        assertEquals(270, frameRotation(base, portraitCommitted, 270))
    }

    @Test
    fun `frameRotation portrait committed 90 class change falls back to committed`() {
        // Current 0 would be landscape dims (differs) -> fall back to committed rotation 90.
        assertEquals(90, frameRotation(base, portraitCommitted, 0))
        assertEquals(90, frameRotation(base, portraitCommitted, 180))
    }

    @Test
    fun `frameRotation landscape committed same rotation keeps current`() {
        assertEquals(0, frameRotation(base, landscapeCommitted, 0))
    }

    @Test
    fun `frameRotation landscape committed 180 flip uses current rotation`() {
        // 0 -> 180 is a 180° flip; dims stay landscape, so track the flip to stay upright.
        assertEquals(180, frameRotation(base, landscapeCommitted, 180))
    }

    @Test
    fun `frameRotation landscape committed 90 class change falls back to committed`() {
        // Current 90 would be portrait dims (differs) -> fall back to committed rotation 0.
        assertEquals(0, frameRotation(base, landscapeCommitted, 90))
        assertEquals(0, frameRotation(base, landscapeCommitted, 270))
    }
}
