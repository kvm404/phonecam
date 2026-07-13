package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class PairingPayloadTest {

    private fun validJson(
        v: Int = 1,
        transport: String = "rtp-h264",
        control: String = "http://192.168.1.5:8765",
        rtp: String = "192.168.1.5:5004",
        session: String = "sess-123",
        token: String = "tok-abc",
        expires: String = "2999-01-01T00:00:00Z",
        width: Int = 1280,
        height: Int = 720,
        fps: Int = 30,
    ): String = """
        {"v":$v,"name":"laptop","control":"$control","rtp":"$rtp",
         "session":"$session","token":"$token","expires":"$expires",
         "transport":"$transport","video":{"width":$width,"height":$height,"fps":$fps}}
    """.trimIndent()

    @Test
    fun `parses a valid payload`() {
        val payload = PairingPayload.parse(validJson())
        assertEquals(1, payload.version)
        assertEquals("laptop", payload.name)
        assertEquals("http://192.168.1.5:8765", payload.control)
        assertEquals("192.168.1.5", payload.rtpHost)
        assertEquals(5004, payload.rtpPort)
        assertEquals("sess-123", payload.session)
        assertEquals("tok-abc", payload.token)
        assertEquals("rtp-h264", payload.transport)
        assertEquals(1280, payload.video.width)
        assertEquals(720, payload.video.height)
        assertEquals(30, payload.video.fps)
    }

    @Test
    fun `unknown extra keys still parse`() {
        val json = """
            {"v":1,"name":"laptop","control":"http://10.0.0.2:8765","rtp":"10.0.0.2:5004",
             "session":"s","token":"t","expires":"2999-01-01T00:00:00Z","transport":"rtp-h264",
             "video":{"width":1280,"height":720,"fps":30,"codec":"h264"},
             "extra":"ignored","future_field":42}
        """.trimIndent()
        val payload = PairingPayload.parse(json)
        assertEquals("s", payload.session)
        assertEquals(720, payload.video.height)
    }

    @Test
    fun `rejects non-json`() {
        val e = assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse("not json at all")
        }
        assertTrue(e.message!!.contains("JSON"))
    }

    @Test
    fun `rejects wrong version`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(v = 2))
        }
    }

    @Test
    fun `rejects wrong transport`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(transport = "rtp/udp"))
        }
    }

    @Test
    fun `rejects non-http control`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(control = "ftp://192.168.1.5:8765"))
        }
    }

    @Test
    fun `rejects rtp without port`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(rtp = "192.168.1.5"))
        }
    }

    @Test
    fun `rejects rtp with non-numeric port`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(rtp = "192.168.1.5:abc"))
        }
    }

    @Test
    fun `rejects blank session`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(session = ""))
        }
    }

    @Test
    fun `rejects blank token`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(token = ""))
        }
    }

    @Test
    fun `rejects zero video dimension`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(width = 0))
        }
    }

    @Test
    fun `rejects zero fps`() {
        assertThrows(IllegalArgumentException::class.java) {
            PairingPayload.parse(validJson(fps = 0))
        }
    }
}
