package com.kvm404.phonecam.streaming

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * JVM tests for the preset bitrate table and the loss-adaptation state machine.
 */
class BitrateControllerTest {

    private var nowMs = 0L

    private fun controller(capBps: Int) =
        BitrateController(capBps = capBps, nowMs = { nowMs })

    private fun closeBadWindow(c: BitrateController) {
        repeat(BitrateController.DROP_THRESHOLD) { c.onInputDrop() }
        nowMs += 1_000L
        c.tick()
    }

    @Test
    fun `table values match the committed presets`() {
        assertEquals(4_000_000, BitrateController.targetFor(1280, 720, 30))
        assertEquals(2_500_000, BitrateController.targetFor(1280, 720, 15))
        assertEquals(2_500_000, BitrateController.targetFor(960, 540, 30))
        assertEquals(1_500_000, BitrateController.targetFor(960, 540, 15))
        assertEquals(1_200_000, BitrateController.targetFor(640, 360, 30))
        assertEquals(700_000, BitrateController.targetFor(640, 360, 15))
    }

    @Test
    fun `unknown canvas falls back by pixel count and fps bucket`() {
        assertEquals(4_000_000, BitrateController.targetFor(1920, 1080, 30))
        assertEquals(700_000, BitrateController.targetFor(320, 180, 15))
        assertEquals(1_200_000, BitrateController.targetFor(320, 180, 30))
    }

    @Test
    fun `two consecutive bad windows step bitrate down`() {
        val c = controller(1_200_000)
        closeBadWindow(c)
        assertEquals(1_200_000, c.bitrate())
        assertFalse(c.consumeApplyBitrate())
        assertFalse(c.consumeForceSync())

        closeBadWindow(c)
        assertEquals(840_000, c.bitrate())
        assertTrue(c.consumeApplyBitrate())
        assertTrue(c.consumeForceSync())
        assertFalse(c.consumeForceSync())
        assertEquals(BitrateController.DEGRADED_SYNC_SECONDS, c.syncIntervalSeconds())
    }

    @Test
    fun `step down never goes below the floor`() {
        val c = controller(700_000)
        closeBadWindow(c)
        closeBadWindow(c)
        assertEquals(490_000, c.bitrate())
        closeBadWindow(c)
        closeBadWindow(c)
        assertEquals(BitrateController.FLOOR_BPS, c.bitrate())
        assertTrue(c.consumeApplyBitrate())
        closeBadWindow(c)
        closeBadWindow(c)
        assertEquals(BitrateController.FLOOR_BPS, c.bitrate())
        assertFalse(c.consumeApplyBitrate())
    }

    @Test
    fun `two more bad windows at the floor skip every other encode`() {
        val c = controller(700_000)
        repeat(4) { closeBadWindow(c) }
        assertEquals(BitrateController.FLOOR_BPS, c.bitrate())
        assertFalse(c.skipEveryOther())

        closeBadWindow(c)
        closeBadWindow(c)
        assertTrue(c.skipEveryOther())
        assertFalse(c.shouldSkipEncode())
        assertTrue(c.shouldSkipEncode())
        assertFalse(c.shouldSkipEncode())
        assertTrue(c.shouldSkipEncode())
    }

    @Test
    fun `ten healthy seconds step bitrate up and cap at the preset`() {
        val c = controller(4_000_000)
        closeBadWindow(c)
        closeBadWindow(c)
        assertEquals(2_800_000, c.bitrate())
        c.consumeApplyBitrate()
        c.consumeForceSync()

        nowMs += 10_000L
        c.tick()
        assertEquals(3_220_000, c.bitrate())
        assertTrue(c.consumeApplyBitrate())
        assertFalse(c.skipEveryOther())

        nowMs += 10_000L
        c.tick()
        assertEquals(3_703_000, c.bitrate())

        nowMs += 10_000L
        c.tick()
        assertEquals(4_000_000, c.bitrate())
        assertEquals(BitrateController.HEALTHY_SYNC_SECONDS, c.syncIntervalSeconds())

        nowMs += 10_000L
        c.tick()
        assertEquals(4_000_000, c.bitrate())
    }

    @Test
    fun `stale last rtp steps down without waiting for drops`() {
        val c = controller(1_200_000)
        c.noteReceiverAge(500)
        assertEquals(840_000, c.bitrate())
        assertTrue(c.consumeForceSync())
        assertTrue(c.consumeApplyBitrate())
        assertEquals(BitrateController.DEGRADED_SYNC_SECONDS, c.syncIntervalSeconds())
    }

    @Test
    fun `step up requires last rtp under 200 when age is known`() {
        val c = controller(1_200_000)
        c.noteReceiverAge(500)
        assertEquals(840_000, c.bitrate())
        c.consumeForceSync()
        c.consumeApplyBitrate()

        c.noteReceiverAge(300)
        nowMs += 10_000L
        c.tick()
        assertEquals(840_000, c.bitrate())

        c.noteReceiverAge(100)
        nowMs += 10_000L
        c.tick()
        assertEquals(966_000, c.bitrate())
    }

    @Test
    fun `requestKeyframe is a one-shot and does not latch the degraded cadence`() {
        val c = controller(4_000_000)
        assertEquals(BitrateController.HEALTHY_SYNC_SECONDS, c.syncIntervalSeconds())
        assertFalse(c.consumeForceSync())

        c.noteRequestKeyframe()
        assertEquals(BitrateController.HEALTHY_SYNC_SECONDS, c.syncIntervalSeconds())
        assertTrue(c.consumeForceSync())
        assertFalse(c.consumeForceSync())
    }

    @Test
    fun `stale last rtp above 400 ms uses a one second cadence`() {
        val c = controller(4_000_000)
        c.noteReceiverAge(401)
        assertEquals(4_000_000, c.bitrate())
        assertEquals(BitrateController.DEGRADED_SYNC_SECONDS, c.syncIntervalSeconds())

        c.noteReceiverAge(100)
        assertEquals(BitrateController.HEALTHY_SYNC_SECONDS, c.syncIntervalSeconds())
    }

    @Test
    fun `restore methods rearm bitrate and sync after a failed apply`() {
        val c = controller(1_200_000)
        closeBadWindow(c)
        closeBadWindow(c)
        assertTrue(c.consumeApplyBitrate())
        assertTrue(c.consumeForceSync())
        c.restoreApplyBitrate()
        c.restoreForceSync()
        assertTrue(c.consumeApplyBitrate())
        assertTrue(c.consumeForceSync())
    }

    @Test
    fun `step up at the floor turns skip-every-other off`() {
        val c = controller(700_000)
        repeat(6) { closeBadWindow(c) }
        assertTrue(c.skipEveryOther())
        assertEquals(BitrateController.FLOOR_BPS, c.bitrate())
        c.consumeForceSync()
        c.consumeApplyBitrate()

        nowMs += 10_000L
        c.tick()
        assertFalse(c.skipEveryOther())
        assertEquals(460_000, c.bitrate())
        assertFalse(c.shouldSkipEncode())
        assertFalse(c.shouldSkipEncode())
    }
}
