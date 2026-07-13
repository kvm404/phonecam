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

    /** Fake that records calls and returns scripted results. */
    private class FakeControlClient(
        val pairResult: PairResult = PairResult.Accepted(approved = false, session = "sess-123"),
        val approveAfter: Int = 0,
        val statusResultOverride: StatusResult? = null,
    ) : ControlClient {
        var pairCalls = 0
        var statusCalls = 0

        override fun pair(
            payload: PairingPayload,
            phone: PhoneIdentity,
            rtpPort: Int,
            ssrc: Long,
        ): PairResult {
            pairCalls++
            return pairResult
        }

        override fun status(payload: PairingPayload): StatusResult {
            statusCalls++
            statusResultOverride?.let { return it }
            val approved = statusCalls >= approveAfter
            return StatusResult.Ok(approved = approved, session = "sess-123")
        }
    }

    @Test
    fun `happy path reaches Paired after N polls`() = runTest {
        val client = FakeControlClient(approveAfter = 3)
        val controller = PairingController(
            client = client,
            phone = phone,
            rtp = rtp,
            clock = { 0L },
            pollIntervalMs = 1000L,
        )

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue("expected Paired, was $state", state is PairingState.Paired)
        assertEquals(futurePayload, (state as PairingState.Paired).payload)
        assertEquals(1, client.pairCalls)
        assertEquals(3, client.statusCalls)
    }

    @Test
    fun `pair error becomes Failed`() = runTest {
        val client = FakeControlClient(pairResult = PairResult.Failure("invalid pairing token"))
        val controller = PairingController(client, phone, rtp, clock = { 0L })

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue(state is PairingState.Failed)
        assertEquals("invalid pairing token", (state as PairingState.Failed).message)
        assertEquals(0, client.statusCalls)
    }

    @Test
    fun `status error becomes Failed`() = runTest {
        val client = FakeControlClient(statusResultOverride = StatusResult.Failure("laptop gone"))
        val controller = PairingController(client, phone, rtp, clock = { 0L })

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
            clock = { Long.MAX_VALUE },
        )

        controller.run(futurePayload)

        val state = controller.state.value
        assertTrue(state is PairingState.Failed)
        assertEquals(PairingController.EXPIRED_MESSAGE, (state as PairingState.Failed).message)
        assertEquals(0, client.pairCalls)
        assertEquals(0, client.statusCalls)
    }
}
