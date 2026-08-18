package com.kvm404.phonecam.pairing

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class HomeReconnectTest {

    private val laptop = TrustedLaptop(
        laptopId = "lid-1",
        name = "arch",
        control = "http://10.0.0.2:47470",
        rtp = "10.0.0.2:47471",
        secret = "stored-secret",
        lastSeen = "2026-08-18T12:00:00Z",
    )
    private val phone = PhoneIdentity(id = "phone-1", name = "Pixel")
    private val video = VideoProfile(1280, 720, 30)
    private val rtp = RtpIdentity(ssrc = 99L, sourcePort = 40100)

    private class FakeControlClient(
        private val handler: (ReconnectRequest) -> ReconnectResult,
    ) : ControlClient {
        val requests = mutableListOf<ReconnectRequest>()

        override fun pair(
            payload: PairingPayload,
            phone: PhoneIdentity,
            rtpPort: Int,
            ssrc: Long,
            video: VideoProfile,
            camera: String?,
        ): PairResult = PairResult.Failure("unused")

        override fun status(payload: PairingPayload): StatusResult =
            StatusResult.Failure("unused")

        override fun reconnect(request: ReconnectRequest): ReconnectResult {
            requests.add(request)
            return handler(request)
        }
    }

    @Test
    fun `reconnect from Home sends pairing secret`() {
        val client = FakeControlClient {
            ReconnectResult.Ok(
                approved = true,
                session = "sess-new",
                resumeToken = "resume-new",
                control = "http://10.0.0.2:47470",
                rtp = "10.0.0.2:47471",
            )
        }
        val result = HomeReconnect(client).connect(
            laptop, phone, rtp, video, camera = "front",
        )
        assertTrue(result is HomeReconnectResult.Ready)
        val ready = result as HomeReconnectResult.Ready
        assertEquals("resume-new", ready.resumeToken)
        assertEquals("stored-secret", ready.pairingSecret)
        assertEquals("lid-1", ready.payload.laptopId)
        assertEquals(1, client.requests.size)
        val sent = client.requests[0]
        assertEquals("stored-secret", sent.pairingSecret)
        assertEquals(null, sent.resumeToken)
        assertEquals(40100, sent.rtpPort)
        assertEquals(99L, sent.ssrc)
        assertEquals("http://10.0.0.2:47470", sent.payload.control)
        assertEquals("front", sent.camera)
    }

    @Test
    fun `failure message tells the user to scan`() {
        val client = FakeControlClient { ReconnectResult.Failure("could not reach laptop") }
        val result = HomeReconnect(client).connect(laptop, phone, rtp, video)
        assertTrue(result is HomeReconnectResult.Failure)
        assertEquals(
            "Can't reach arch — scan the QR",
            (result as HomeReconnectResult.Failure).message,
        )
    }

    @Test
    fun `different phone is in use`() {
        assertEquals(
            HomeReconnect.inUse(),
            HomeReconnect.failureMessage("arch", "a different phone is already approved"),
        )
    }
}
