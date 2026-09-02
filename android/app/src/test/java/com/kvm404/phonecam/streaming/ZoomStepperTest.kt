package com.kvm404.phonecam.streaming

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * JVM tests for the LIVE-screen zoom math: 0.25x stepping, clamping, reset-to-1x,
 * readout formatting, and the hide predicate for lenses without a zoom range.
 */
class ZoomStepperTest {

    // ------------------------------------------------------------------ stepping

    @Test
    fun `step up adds exactly 0_25 per press`() {
        assertEquals(1.25f, ZoomStepper.stepUp(1.0f, maxRatio = 8.0f))
        assertEquals(1.5f, ZoomStepper.stepUp(1.25f, maxRatio = 8.0f))
        assertEquals(1.75f, ZoomStepper.stepUp(1.5f, maxRatio = 8.0f))
        assertEquals(2.0f, ZoomStepper.stepUp(1.75f, maxRatio = 8.0f))
    }

    @Test
    fun `step down subtracts exactly 0_25 per press`() {
        assertEquals(1.75f, ZoomStepper.stepDown(2.0f, minRatio = 1.0f))
        assertEquals(1.5f, ZoomStepper.stepDown(1.75f, minRatio = 1.0f))
        assertEquals(1.25f, ZoomStepper.stepDown(1.5f, minRatio = 1.0f))
        assertEquals(1.0f, ZoomStepper.stepDown(1.25f, minRatio = 1.0f))
    }

    @Test
    fun `step up clamps at max ratio`() {
        assertEquals(2.0f, ZoomStepper.stepUp(1.9f, maxRatio = 2.0f))
        assertEquals(2.0f, ZoomStepper.stepUp(2.0f, maxRatio = 2.0f))
        // A front lens that stops just past 1x.
        assertEquals(1.5f, ZoomStepper.stepUp(1.4f, maxRatio = 1.5f))
    }

    @Test
    fun `step down clamps at min ratio`() {
        assertEquals(1.0f, ZoomStepper.stepDown(1.1f, minRatio = 1.0f))
        assertEquals(1.0f, ZoomStepper.stepDown(1.0f, minRatio = 1.0f))
        // An ultra-wide lens that starts below 1x.
        assertEquals(0.8f, ZoomStepper.stepDown(0.85f, minRatio = 0.8f))
    }

    // ------------------------------------------------------------------ reset

    @Test
    fun `reset target is 1x inside the range`() {
        assertEquals(1.0f, ZoomStepper.resetTarget(minRatio = 1.0f, maxRatio = 8.0f))
        assertEquals(1.0f, ZoomStepper.resetTarget(minRatio = 0.8f, maxRatio = 4.0f))
    }

    @Test
    fun `reset target clamps into a shrunken range`() {
        // Front lens that stops at 1.5 still resets to 1x.
        assertEquals(1.0f, ZoomStepper.resetTarget(minRatio = 1.0f, maxRatio = 1.5f))
        // Degenerate single-ratio lens.
        assertEquals(1.0f, ZoomStepper.resetTarget(minRatio = 1.0f, maxRatio = 1.0f))
    }

    // ------------------------------------------------------------------ bounds for buttons

    @Test
    fun `zoom in enabled only below max`() {
        assertTrue(ZoomStepper.canZoomIn(1.0f, maxRatio = 2.0f))
        assertTrue(ZoomStepper.canZoomIn(1.75f, maxRatio = 2.0f))
        assertFalse(ZoomStepper.canZoomIn(2.0f, maxRatio = 2.0f))
        // Hair below max is still the bound (within the compare epsilon).
        assertFalse(ZoomStepper.canZoomIn(2.0f - 1e-5f, maxRatio = 2.0f))
    }

    @Test
    fun `zoom out enabled only above min`() {
        assertTrue(ZoomStepper.canZoomOut(2.0f, minRatio = 1.0f))
        assertTrue(ZoomStepper.canZoomOut(1.25f, minRatio = 1.0f))
        assertFalse(ZoomStepper.canZoomOut(1.0f, minRatio = 1.0f))
        // Hair above min is still the bound (within the compare epsilon).
        assertFalse(ZoomStepper.canZoomOut(1.0f + 1e-5f, minRatio = 1.0f))
    }

    @Test
    fun `reset enabled only away from the reset target`() {
        assertTrue(ZoomStepper.canReset(1.5f, minRatio = 1.0f, maxRatio = 2.0f))
        assertFalse(ZoomStepper.canReset(1.0f, minRatio = 1.0f, maxRatio = 2.0f))
        // Range clamped above 1x: reset target is the min, so sitting there disables it.
        assertTrue(ZoomStepper.canReset(2.0f, minRatio = 1.5f, maxRatio = 4.0f))
        assertFalse(ZoomStepper.canReset(1.5f, minRatio = 1.5f, maxRatio = 4.0f))
    }

    // ------------------------------------------------------------------ hide predicate

    @Test
    fun `hide the zoom row when max ratio is 1x or below`() {
        assertFalse(ZoomStepper.shouldShow(1.0f))
        assertFalse(ZoomStepper.shouldShow(0.9f))
    }

    @Test
    fun `show the zoom row as soon as max ratio exceeds 1x`() {
        assertTrue(ZoomStepper.shouldShow(1.0001f))
        assertTrue(ZoomStepper.shouldShow(1.5f))
        assertTrue(ZoomStepper.shouldShow(8.0f))
    }

    // ------------------------------------------------------------------ formatting

    @Test
    fun `format drops the trailing zero on whole ratios`() {
        assertEquals("1x", ZoomStepper.format(1.0f))
        assertEquals("2x", ZoomStepper.format(2.0f))
        assertEquals("4x", ZoomStepper.format(4.0f))
    }

    @Test
    fun `format keeps meaningful decimals`() {
        assertEquals("1.25x", ZoomStepper.format(1.25f))
        assertEquals("1.5x", ZoomStepper.format(1.5f))
        assertEquals("1.75x", ZoomStepper.format(1.75f))
    }

    @Test
    fun `format never leaks float noise`() {
        // 1.05f * 100 is 105.00001 in float; the readout must stay clean.
        assertEquals("1.05x", ZoomStepper.format(1.05f))
        assertEquals("0.8x", ZoomStepper.format(0.8f))
    }
}
