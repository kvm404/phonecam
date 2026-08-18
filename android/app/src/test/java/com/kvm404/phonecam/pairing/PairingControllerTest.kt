package com.kvm404.phonecam.pairing

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class PairingControllerTest {

    private val futurePayload = PairingPayload(
        version = 1,
        name = "laptop",
        control = "http://10.0.0.2:8765",
        rtp = "10.0.0.2:5004",
        rtpHost = "10.0.0.2",
        rtpPort = 5004,
        session = "sess-123",
        token = "tok-abc",
        expires = "2999-01-01T00:00:00Z",
        transport = "rtp-h264",
        video = VideoProfile(1280, 720, 30),
    )

    private val rtp = RtpIdentity(ssrc = 123L, sourcePort = 40000)
    private val phone = PhoneIdentity(id = "p1", name = "Pixel")
    private val video = VideoProfile(720, 1280, 30)

    /** Fake that records calls and returns scripted results. */
    private class FakeControlClient(
        val pairResult: PairResult = PairResult.Accepted(approved = false, session = "sess-123"),
        val approveAfter: Int = 0,
        val statusResultOverride: StatusResult? = null,
    ) : ControlClient {
        var pairCalls = 0
        var statusCalls = 0
        var lastVideo: VideoProfile? = null
        var lastCamera: String? = null

        override fun pair(
            payload: PairingPayload,
            phone: PhoneIdentity,
            rtpPort: Int,
            ssrc: Long,
            video: VideoProfile,
            camera: String?,
        ): PairResult {
            pairCalls++
            lastVideo = video
            lastCamera = camera
            return pairResult
        }

        override fun status(payload: PairingPayload): StatusResult {
            statusCalls++
            statusResultOverride?.let { return it }
            val approved = statusCalls >= approveAfter
            return StatusResult.Ok(
                approved = approved,
                session = "sess-123",
                resumeToken = if (approved) "status-resume" else null,
                pairingSecret = if (approved) "status-secret" else null,
            )
        }

        override fun reconnect(request: ReconnectRequest): ReconnectResult =
            ReconnectResult.Failure("unused")
    }

    @Test
    fun `happy path reaches Paired after N polls`() = runTest {
        val client = FakeControlClient(approveAfter = 3)
        val controller = PairingController(
            client = client,
            phone = phone,
            rtp = rtp,
            video = video,
            clock = { 0L },
            pollIntervalMs = 1000L,
        )

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue("expected Paired, was $state", state is PairingState.Paired)
        assertEquals(futurePayload, (state as PairingState.Paired).payload)
        assertEquals("status-resume", state.resumeToken)
        assertEquals("status-secret", state.pairingSecret)
        assertEquals(1, client.pairCalls)
        assertEquals(3, client.statusCalls)
        // The effective (rotated) profile is forwarded to POST /pair.
        assertEquals(video, client.lastVideo)
    }

    @Test
    fun `approved pair short-circuits without status poll`() = runTest {
        val client = FakeControlClient(
            pairResult = PairResult.Accepted(
                approved = true,
                session = "sess-123",
                resumeToken = "pair-resume",
                pairingSecret = null,
            ),
        )
        val controller = PairingController(client, phone, rtp, video, clock = { 0L })

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue("expected Paired, was $state", state is PairingState.Paired)
        state as PairingState.Paired
        assertEquals(futurePayload, state.payload)
        assertEquals("pair-resume", state.resumeToken)
        assertEquals(null, state.pairingSecret)
        assertEquals(1, client.pairCalls)
        assertEquals(0, client.statusCalls)
    }

    @Test
    fun `pair error becomes Failed`() = runTest {
        val client = FakeControlClient(pairResult = PairResult.Failure("invalid pairing token"))
        val controller = PairingController(client, phone, rtp, video, clock = { 0L })

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue(state is PairingState.Failed)
        assertEquals("invalid pairing token", (state as PairingState.Failed).message)
        assertEquals(0, client.statusCalls)
    }

    @Test
    fun `status error becomes Failed`() = runTest {
        val client = FakeControlClient(statusResultOverride = StatusResult.Failure("laptop gone"))
        val controller = PairingController(client, phone, rtp, video, clock = { 0L })

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue(state is PairingState.Failed)
        assertEquals("laptop gone", (state as PairingState.Failed).message)
    }

    @Test
    fun `expired payload becomes Failed without network calls`() = runTest {
        val client = FakeControlClient()
        val controller = PairingController(
            client = client,
            phone = phone,
            rtp = rtp,
            video = video,
            clock = { Long.MAX_VALUE },
        )

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue(state is PairingState.Failed)
        assertEquals(PairingController.EXPIRED_MESSAGE, (state as PairingState.Failed).message)
        assertEquals(0, client.pairCalls)
        assertEquals(0, client.statusCalls)
    }

    @Test
    fun `pair forwards camera so status can echo it from first handshake`() = runTest {
        val client = FakeControlClient(
            pairResult = PairResult.Accepted(approved = true, session = "sess-123"),
        )
        val controller = PairingController(
            client, phone, rtp, video, clock = { 0L }, camera = "front",
        )

        controller.run(futurePayload)

        assertTrue(controller.state.value is PairingState.Paired)
        assertEquals("front", client.lastCamera)
    }
}
