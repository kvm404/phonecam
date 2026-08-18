package com.kvm404.phonecam.pairing

import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.util.concurrent.atomic.AtomicBoolean

/** Live-stream health after pairing. Distinct from [PairingState]. */
sealed interface StreamHealth {
    data object Live : StreamHealth
    data class Reconnecting(val attempt: Int) : StreamHealth
    data class Failed(val message: String) : StreamHealth
}

/** Credentials and canvas the service keeps after the pairing handoff. */
data class SessionCredentials(
    val payload: PairingPayload,
    val resumeToken: String?,
    val pairingSecret: String?,
    val profile: VideoProfile,
    val phone: PhoneIdentity,
)

/** RTP source announced on this reconnect attempt. */
data class RtpEndpoint(
    val port: Int,
    val ssrc: Long,
)

/**
 * Pure-Kotlin single-flight reconnect loop. Free of Android imports so it runs
 * on the JVM. The service owns CameraX / MediaCodec / sockets; this class only
 * decides when to POST /reconnect, which credentials to send, and when to give up.
 *
 * Give-up is 60 s of failed attempts while Wi-Fi is available — not wall-clock
 * from the first loss. A long Wi-Fi outage does not burn the timer.
 *
 * @param clock epoch millis; injected for deterministic tests.
 * @param delayMs backoff / wifi-wait; default [delay] so tests use virtual time.
 */
class ReconnectController(
    private val client: ControlClient,
    private var creds: SessionCredentials,
    private val clock: () -> Long = { System.currentTimeMillis() },
    private val wifiAvailable: () -> Boolean = { true },
    private val resolveIdentity: () -> RtpEndpoint,
    private val camera: () -> String? = { null },
    private val onSuccess: (ReconnectResult.Ok, RtpEndpoint) -> Unit = { _, _ -> },
    private val delayMs: suspend (Long) -> Unit = { delay(it) },
) {
    private val _health = MutableStateFlow<StreamHealth>(StreamHealth.Live)
    val health: StateFlow<StreamHealth> = _health.asStateFlow()

    private val inFlight = AtomicBoolean(false)

    @Volatile
    private var cancelled = false

    fun credentials(): SessionCredentials = creds

    fun cancel() {
        cancelled = true
    }

    /**
     * Run the reconnect loop to Live or Failed. A second call while a loop is
     * already running is a no-op. Give-up is terminal: [start] will not run
     * another 60 s window after [StreamHealth.Failed].
     */
    suspend fun start() {
        if (cancelled || _health.value is StreamHealth.Failed) return
        if (!inFlight.compareAndSet(false, true)) return
        try {
            runLoop()
        } finally {
            inFlight.set(false)
        }
    }

    private suspend fun runLoop() {
        var attempt = 0
        var wifiFailStartedAt: Long? = null
        var backoffIndex = 0

        while (!cancelled) {
            if (!wifiAvailable()) {
                wifiFailStartedAt = null
                if (_health.value !is StreamHealth.Reconnecting) {
                    _health.value = StreamHealth.Reconnecting(attempt.coerceAtLeast(1))
                }
                delayMs(WIFI_POLL_MS)
                continue
            }

            attempt++
            _health.value = StreamHealth.Reconnecting(attempt)

            val identity = try {
                resolveIdentity()
            } catch (_: Exception) {
                val after = giveUpAfterFailure(wifiFailStartedAt)
                wifiFailStartedAt = after.first
                if (after.second) return
                backoffIndex = sleepBackoff(backoffIndex)
                continue
            }

            when (val result = postReconnect(identity)) {
                is ReconnectResult.Ok -> {
                    acceptOk(result, identity)
                    _health.value = StreamHealth.Live
                    return
                }
                is ReconnectResult.Failure -> {
                    val after = giveUpAfterFailure(wifiFailStartedAt)
                    wifiFailStartedAt = after.first
                    if (after.second) return
                    backoffIndex = sleepBackoff(backoffIndex)
                }
            }
        }
    }

    /**
     * POST /reconnect once with [identity] so /status camera matches a
     * first-bind fallback. Does not recreate RTP, does not enter
     * Reconnecting, and does not give up. No-op while a loop is running
     * or after Failed — that loop already sends [camera].
     */
    fun reportCamera(identity: RtpEndpoint) {
        if (cancelled || _health.value !is StreamHealth.Live) return
        if (!inFlight.compareAndSet(false, true)) return
        try {
            when (val result = postReconnect(identity)) {
                is ReconnectResult.Ok -> acceptOk(result, identity)
                is ReconnectResult.Failure -> Unit
            }
        } finally {
            inFlight.set(false)
        }
    }

    private fun postReconnect(identity: RtpEndpoint): ReconnectResult {
        val request = ReconnectRequest(
            payload = creds.payload,
            phone = creds.phone,
            rtpPort = identity.port,
            ssrc = identity.ssrc,
            video = creds.profile,
            resumeToken = creds.resumeToken,
            pairingSecret = creds.pairingSecret,
            camera = camera(),
        )
        return try {
            client.reconnect(request)
        } catch (t: Exception) {
            ReconnectResult.Failure(t.message ?: "reconnect failed")
        }
    }

    private fun acceptOk(result: ReconnectResult.Ok, identity: RtpEndpoint) {
        if (result.resumeToken.isNotBlank()) {
            creds = creds.copy(resumeToken = result.resumeToken)
        }
        if (result.session.isNotBlank()) {
            creds = creds.copy(
                payload = creds.payload.copy(session = result.session),
            )
        }
        onSuccess(result, identity)
    }

    /**
     * @return updated window start and whether the loop should give up.
     */
    private fun giveUpAfterFailure(windowStart: Long?): Pair<Long?, Boolean> {
        if (!wifiAvailable()) return null to false
        val now = clock()
        val start = windowStart ?: now
        if (now - start >= GIVE_UP_MS) {
            cancelled = true
            _health.value = StreamHealth.Failed(GIVE_UP_MESSAGE)
            return start to true
        }
        return start to false
    }

    private suspend fun sleepBackoff(index: Int): Int {
        val wait = BACKOFF_MS[index.coerceAtMost(BACKOFF_MS.lastIndex)]
        delayMs(wait)
        return (index + 1).coerceAtMost(BACKOFF_MS.lastIndex)
    }

    companion object {
        const val GIVE_UP_MESSAGE = "lost laptop — rescan the QR"
        const val GIVE_UP_MS = 60_000L
        val BACKOFF_MS = longArrayOf(500L, 1_000L, 2_000L, 4_000L, 8_000L)
        private const val WIFI_POLL_MS = 500L

        /** Watchdog: skip session/send-failure triggers while a loop is already running. */
        fun shouldStartOnStatus(
            health: StreamHealth,
            approved: Boolean,
            statusSession: String,
            liveSession: String?,
        ): Boolean {
            if (health is StreamHealth.Reconnecting) return false
            if (!approved) return true
            return statusSession.isNotBlank() &&
                liveSession != null &&
                statusSession != liveSession
        }

        fun shouldStartOnSendFailures(
            health: StreamHealth,
            failures: Int,
            lastSeen: Int,
        ): Boolean = health !is StreamHealth.Reconnecting && failures > lastSeen
    }
}
