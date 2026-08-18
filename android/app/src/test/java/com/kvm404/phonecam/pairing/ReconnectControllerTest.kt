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
    fun `give-up is terminal and a later start does not POST`() = runTest {
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
        val posts = client.requests.size
        assertTrue(posts > 1)

        controller.start()
        assertEquals(posts, client.requests.size)
        assertTrue(controller.health.value is StreamHealth.Failed)
        assertEquals(
            "lost laptop — rescan the QR",
            (controller.health.value as StreamHealth.Failed).message,
        )
    }

    @Test
    fun `watchdog ignores session mismatch and send failures while Reconnecting`() {
        val reconnecting = StreamHealth.Reconnecting(2)
        assertTrue(
            !ReconnectController.shouldStartOnStatus(
                reconnecting, approved = false, statusSession = "b", liveSession = "a",
            ),
        )
        assertTrue(
            !ReconnectController.shouldStartOnSendFailures(reconnecting, failures = 4, lastSeen = 1),
        )
        assertTrue(
            ReconnectController.shouldStartOnStatus(
                StreamHealth.Live, approved = false, statusSession = "a", liveSession = "a",
            ),
        )
        // Compare to the live session after GET, not a pre-request snapshot.
        assertTrue(
            !ReconnectController.shouldStartOnStatus(
                StreamHealth.Live, approved = true, statusSession = "new", liveSession = "new",
            ),
        )
        assertTrue(
            ReconnectController.shouldStartOnStatus(
                StreamHealth.Live, approved = true, statusSession = "new", liveSession = "old",
            ),
        )
        assertTrue(
            ReconnectController.shouldStartOnSendFailures(
                StreamHealth.Live, failures = 3, lastSeen = 1,
            ),
        )
        assertTrue(
            !ReconnectController.shouldStartOnSendFailures(
                StreamHealth.Live, failures = 3, lastSeen = 3,
            ),
        )
    }

    @Test
    fun `reportCamera posts live pin and stays Live without resolving identity`() = runTest {
        var resolved = 0
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
            creds = creds(),
            clock = { 0L },
            wifiAvailable = { true },
            resolveIdentity = {
                resolved++
                error("reportCamera must keep the live RTP pin")
            },
            camera = { "front" },
        )

        controller.reportCamera(RtpEndpoint(port = 40100, ssrc = 99L))

        assertTrue(controller.health.value is StreamHealth.Live)
        assertEquals(0, resolved)
        assertEquals(1, client.requests.size)
        val sent = client.requests[0]
        assertEquals(40100, sent.rtpPort)
        assertEquals(99L, sent.ssrc)
        assertEquals("front", sent.camera)
        assertEquals("old-resume", sent.resumeToken)
        assertEquals("stored-secret", sent.pairingSecret)
        assertEquals("echoed-resume", controller.credentials().resumeToken)
        assertEquals("new-sess", controller.credentials().payload.session)
    }

    @Test
    fun `reportCamera failure keeps Live`() = runTest {
        val client = FakeControlClient { ReconnectResult.Failure("down") }
        val controller = ReconnectController(
            client = client,
            creds = creds(),
            clock = { 0L },
            wifiAvailable = { true },
            resolveIdentity = { error("unused") },
            camera = { "back" },
        )

        controller.reportCamera(RtpEndpoint(40000, 1L))

        assertTrue(controller.health.value is StreamHealth.Live)
        assertEquals(1, client.requests.size)
        assertEquals("old-resume", controller.credentials().resumeToken)
    }

    @Test
    fun `reportCamera is a no-op while a loop is running`() = runTest {
        var now = 0L
        val client = FakeControlClient { ReconnectResult.Failure("down") }
        val controller = ReconnectController(
            client = client,
            creds = creds(),
            clock = { now },
            wifiAvailable = { true },
            resolveIdentity = { RtpEndpoint(40000, 1L) },
            camera = { "front" },
            delayMs = { ms ->
                now += ms
                kotlinx.coroutines.delay(ms)
            },
        )

        val job = launch { controller.start() }
        advanceTimeBy(100L)
        assertEquals(1, client.requests.size)
        assertTrue(controller.health.value is StreamHealth.Reconnecting)

        controller.reportCamera(RtpEndpoint(40100, 99L))
        assertEquals(1, client.requests.size)
        assertEquals(40000, client.requests[0].rtpPort)

        controller.cancel()
        advanceTimeBy(1_000L)
        advanceUntilIdle()
        job.join()
    }

    @Test
    fun `reportCamera is a no-op after Failed`() = runTest {
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
        assertTrue(controller.health.value is StreamHealth.Failed)
        val posts = client.requests.size
        assertTrue(posts > 1)

        controller.reportCamera(RtpEndpoint(40100, 99L))
        assertEquals(posts, client.requests.size)
        assertTrue(controller.health.value is StreamHealth.Failed)
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
