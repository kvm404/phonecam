package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class TrustedLaptopsTest {

    @Test
    fun `persist replace and forget`() {
        var stored: String? = null
        val store = TrustedLaptops(load = { stored }, save = { stored = it })
        assertTrue(store.list().isEmpty())

        store.upsert(
            TrustedLaptop(
                laptopId = "lid-1",
                name = "arch",
                control = "http://192.168.1.5:47470",
                rtp = "192.168.1.5:47471",
                secret = "secret-a",
                lastSeen = "2026-08-17T12:00:00Z",
            )
        )
        store.upsert(
            TrustedLaptop(
                laptopId = "lid-2",
                name = "other",
                control = "http://10.0.0.2:47470",
                rtp = "10.0.0.2:47471",
                secret = "secret-b",
                lastSeen = "2026-08-18T12:00:00Z",
            )
        )

        var list = store.list()
        assertEquals(2, list.size)
        assertEquals("lid-2", list[0].laptopId)
        assertEquals("lid-1", list[1].laptopId)

        store.upsert(
            TrustedLaptop(
                laptopId = "lid-1",
                name = "arch-renamed",
                control = "http://192.168.1.9:47470",
                rtp = "192.168.1.9:47471",
                secret = "secret-rotated",
                lastSeen = "2026-08-18T13:00:00Z",
            )
        )
        list = store.list()
        assertEquals(2, list.size)
        val updated = list.first { it.laptopId == "lid-1" }
        assertEquals("secret-rotated", updated.secret)
        assertEquals("arch-renamed", updated.name)
        assertEquals("lid-1", list[0].laptopId)

        store.forget("lid-2")
        list = store.list()
        assertEquals(1, list.size)
        assertEquals("lid-1", list[0].laptopId)
        assertEquals("secret-rotated", list[0].secret)
    }
}
