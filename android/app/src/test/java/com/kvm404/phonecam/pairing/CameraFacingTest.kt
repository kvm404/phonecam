package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class CameraFacingTest {

    @Test
    fun `missing or unknown pref is back`() {
        assertEquals(CameraFacing.BACK, CameraFacing.fromPref(null))
        assertEquals(CameraFacing.BACK, CameraFacing.fromPref(""))
        assertEquals(CameraFacing.BACK, CameraFacing.fromPref("wide"))
        assertEquals(CameraFacing.BACK, CameraFacing.fromPref("BACK"))
    }

    @Test
    fun `front pref is preserved`() {
        assertEquals(CameraFacing.FRONT, CameraFacing.fromPref(CameraFacing.FRONT))
    }

    @Test
    fun `canFlip requires both default facings`() {
        val back = 1
        val front = 0
        assertFalse(CameraFacing.canFlip(emptyList(), back, front))
        assertFalse(CameraFacing.canFlip(listOf(back), back, front))
        assertFalse(CameraFacing.canFlip(listOf(front), back, front))
        assertFalse(CameraFacing.canFlip(listOf(back, back, 2), back, front))
        assertTrue(CameraFacing.canFlip(listOf(back, front), back, front))
        assertTrue(CameraFacing.canFlip(listOf(front, back, back, 2), back, front))
    }
}
