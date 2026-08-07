package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test

/**
 * JVM tests for the pure [StreamQuality] preset model: correct 16:9 dims, persisted-key
 * round-tripping, and the default-when-missing/invalid contract.
 */
class StreamQualityTest {

    @Test
    fun `preset dimensions are correct`() {
        assertEquals(1280, StreamQuality.HIGH.width)
        assertEquals(720, StreamQuality.HIGH.height)
        assertEquals(960, StreamQuality.MEDIUM.width)
        assertEquals(540, StreamQuality.MEDIUM.height)
        assertEquals(640, StreamQuality.LOW.width)
        assertEquals(360, StreamQuality.LOW.height)
    }

    @Test
    fun `all presets are 16 by 9`() {
        for (quality in StreamQuality.entries) {
            // 16:9 <=> width * 9 == height * 16
            assertEquals(
                "${quality.name} must be 16:9",
                quality.height * 16,
                quality.width * 9,
            )
        }
    }

    @Test
    fun `all presets are 30 fps`() {
        for (quality in StreamQuality.entries) {
            assertEquals("${quality.name} fps", 30, quality.fps)
        }
    }

    @Test
    fun `toProfile carries dims and fps`() {
        val profile = StreamQuality.MEDIUM.toProfile()
        assertEquals(960, profile.width)
        assertEquals(540, profile.height)
        assertEquals(30, profile.fps)
    }

    @Test
    fun `key round-trips string to enum to string`() {
        for (quality in StreamQuality.entries) {
            assertSame(quality, StreamQuality.fromKey(quality.key))
            assertEquals(quality.key, StreamQuality.fromKey(quality.key).key)
        }
    }

    @Test
    fun `default is HIGH`() {
        assertSame(StreamQuality.HIGH, StreamQuality.DEFAULT)
    }

    @Test
    fun `missing key falls back to HIGH`() {
        assertSame(StreamQuality.HIGH, StreamQuality.fromKey(null))
    }

    @Test
    fun `invalid key falls back to HIGH`() {
        assertSame(StreamQuality.HIGH, StreamQuality.fromKey("ultra"))
        assertSame(StreamQuality.HIGH, StreamQuality.fromKey(""))
        assertSame(StreamQuality.HIGH, StreamQuality.fromKey("HIGH"))
    }

    @Test
    fun `keys are unique and lowercase`() {
        val keys = StreamQuality.entries.map { it.key }
        assertEquals(keys.size, keys.toSet().size)
        for (key in keys) {
            assertEquals(key, key.lowercase())
        }
    }
}
