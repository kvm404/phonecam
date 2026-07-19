package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * JVM tests for the pure sensor-orientation helpers: 45° band quantization (with the unknown
 * sentinel) and the inverse Surface-rotation mapping.
 */
class DeviceOrientationTest {

    // --- quantizeOrientation: 45° bands ---

    @Test
    fun `band centers quantize to their orientation`() {
        assertEquals(0, quantizeOrientation(0, last = 90))
        assertEquals(90, quantizeOrientation(90, last = 0))
        assertEquals(180, quantizeOrientation(180, last = 0))
        assertEquals(270, quantizeOrientation(270, last = 0))
    }

    @Test
    fun `band edges land on the expected side`() {
        // 315..44 -> 0
        assertEquals(0, quantizeOrientation(315, last = 90))
        assertEquals(0, quantizeOrientation(359, last = 90))
        assertEquals(0, quantizeOrientation(44, last = 90))
        // 45..134 -> 90
        assertEquals(90, quantizeOrientation(45, last = 0))
        assertEquals(90, quantizeOrientation(134, last = 0))
        // 135..224 -> 180
        assertEquals(180, quantizeOrientation(135, last = 0))
        assertEquals(180, quantizeOrientation(224, last = 0))
        // 225..314 -> 270
        assertEquals(270, quantizeOrientation(225, last = 0))
        assertEquals(270, quantizeOrientation(314, last = 0))
    }

    @Test
    fun `unknown sentinel keeps the last orientation`() {
        assertEquals(270, quantizeOrientation(-1, last = 270))
        assertEquals(0, quantizeOrientation(-1, last = 0))
    }

    @Test
    fun `degrees wrap modulo 360`() {
        assertEquals(0, quantizeOrientation(360, last = 90))
        assertEquals(90, quantizeOrientation(405, last = 0))
    }

    // --- deviceOrientationToSurfaceRotation: standard inverse mapping ---

    @Test
    fun `orientation maps to inverse Surface rotation`() {
        assertEquals(0, deviceOrientationToSurfaceRotation(0)) // ROTATION_0
        assertEquals(3, deviceOrientationToSurfaceRotation(90)) // ROTATION_270
        assertEquals(2, deviceOrientationToSurfaceRotation(180)) // ROTATION_180
        assertEquals(1, deviceOrientationToSurfaceRotation(270)) // ROTATION_90
    }
}
