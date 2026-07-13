package com.kvm404.phonecam.pairing

import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class RtpIdentityTest {

    @Test
    fun `ssrc is non-zero and within uint32 range`() {
        repeat(200) {
            val ssrc = RtpIdentity.randomSsrc()
            assertNotEquals(0L, ssrc)
            assertTrue("ssrc out of uint32 range: $ssrc", ssrc in 1L..0xFFFFFFFFL)
        }
    }

    @Test
    fun `picked source port is in valid range`() {
        val port = RtpIdentity.pickSourcePort()
        assertTrue("port out of range: $port", port in 1..65535)
    }

    @Test
    fun `create yields a usable identity`() {
        val identity = RtpIdentity.create()
        assertNotEquals(0L, identity.ssrc)
        assertTrue(identity.sourcePort in 1..65535)
    }
}
