package com.kvm404.phonecam.pairing

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class ReconnectControllerTest {

    private val payload = PairingPayload(
        version = 1,
        name = "laptop",
        control = "http://10.0.0.2:47470",
        rtp = "10.0.0.2:47471",
        rtpHost = "10.0.0.2",
        rtpPort = 47471,
        session = "old-sess",
        token = "tok-abc",
        expires = "2999-01-01T00:00:00Z",
        transport = "rtp-h264",
        video = VideoProfile(1280, 720, 30),
    )

    private val phone = PhoneIdentity(id = "phone-1", name = "Pixel")
    private val profile = VideoProfile(1280, 720, 30)

    private fun creds(
        resume: String? = "old-resume",
        secret: String? = "stored-secret",
        session: String = "old-sess",
    ) = SessionCredentials(
        payload = payload.copy(session = session),
        resumeToken = resume,
        pairingSecret = secret,
        profile = profile,
        phone = phone,
    )

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
        ): PairResult = PairResult.Failure("unused")

        override fun status(payload: PairingPayload): StatusResult =
            StatusResult.Failure("unused")

        override fun reconnect(request: ReconnectRequest): ReconnectResult {
            requests.add(request)
            return handler(request)
        }
    }

    @Test
    fun `session mismatch with stored secret still sends both creds and stays Live`() = runTest {
        val client = FakeControlClient { request ->
            ReconnectResult.Ok(
                approved = true,
                session = "new-sess",
                resumeToken = "echoed-resume",
                control = "http://10.0.0.2:47470",
                rtp = "10.0.0.2:47471",
            )
        }
        val controller = ReconnectController(
            client = client,
            creds = creds(resume = "old-resume", secret = "stored-secret", session = "old-sess"),
            clock = { 0L },
            wifiAvailable = { true },
            resolveIdentity = { RtpEndpoint(port = 40100, ssrc = 99L) },
            camera = { "back" },
        )

        controller.start()

        val health = controller.health.value
        assertTrue("expected Live, was $health", health is StreamHealth.Live)
        assertEquals(1, client.requests.size)
        val sent = client.requests[0]
        assertEquals("old-resume", sent.resumeToken)
        assertEquals("stored-secret", sent.pairingSecret)
        assertEquals(40100, sent.rtpPort)
        assertEquals(99L, sent.ssrc)
        assertEquals(profile, sent.video)
        assertEquals("back", sent.camera)
        assertEquals("phone-1", sent.phone.id)
        assertEquals("echoed-resume", controller.credentials().resumeToken)
        assertEquals("new-sess", controller.credentials().payload.session)
    }

    @Test
    fun `gives up after 60s of failed attempts while wifi is up`() = runTest {
        var now = 0L
        val client = FakeControlClient { ReconnectResult.Failure("down") }
        val controller = ReconnectController(
            client = client,
            creds = creds(),
            clock = { now },
            wifiAvailable = { true },
            resolveIdentity = { RtpEndpoint(40000, 1L) },
            delayMs = { ms ->
                now += ms
                kotlinx.coroutines.delay(ms)
            },
        )

        controller.start()

        val health = controller.health.value
        assertTrue("expected Failed, was $health", health is StreamHealth.Failed)
        assertEquals(
            ReconnectController.GIVE_UP_MESSAGE,
            (health as StreamHealth.Failed).message,
        )
        assertTrue(client.requests.size > 1)
        assertTrue(now >= ReconnectController.GIVE_UP_MS)
    }

    @Test
    fun `wifi outage does not burn the give-up timer`() = runTest {
        var now = 0L
        val client = FakeControlClient { ReconnectResult.Failure("down") }
        val controller = ReconnectController(
            client = client,
            creds = creds(),
            clock = { now },
            wifiAvailable = { false },
            resolveIdentity = { RtpEndpoint(40000, 1L) },
            delayMs = { ms ->
                now += ms
                kotlinx.coroutines.delay(ms)
            },
        )

        val job = launch { controller.start() }
        advanceTimeBy(70_000L)
        assertTrue(
            "expected Reconnecting after 70s without wifi, was ${controller.health.value}",
            controller.health.value is StreamHealth.Reconnecting,
        )
        assertEquals(0, client.requests.size)
        controller.cancel()
        advanceTimeBy(1_000L)
        advanceUntilIdle()
        job.join()
        assertTrue(controller.health.value !is StreamHealth.Failed)
    }

    @Test
    fun `second start is a no-op while a loop is running`() = runTest {
        var now = 0L
        val client = FakeControlClient { ReconnectResult.Failure("down") }
        val controller = ReconnectController(
            client = client,
            creds = creds(),
            clock = { now },
            wifiAvailable = { true },
            resolveIdentity = { RtpEndpoint(40000, 1L) },
            delayMs = { ms ->
                now += ms
                kotlinx.coroutines.delay(ms)
            },
        )

        val job = launch { controller.start() }
        advanceTimeBy(100L)
        assertEquals(1, client.requests.size)
        launch { controller.start() }
        advanceTimeBy(50L)
        assertEquals(1, client.requests.size)
        controller.cancel()
        advanceTimeBy(1_000L)
        advanceUntilIdle()
        job.join()
    }
}
