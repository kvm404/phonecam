package com.kvm404.phonecam.pairing

import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.time.OffsetDateTime

/** States of the pairing handshake. */
sealed interface PairingState {
    data object Idle : PairingState
    data object Pairing : PairingState
    data object WaitingApproval : PairingState
    data class Paired(val payload: PairingPayload) : PairingState
    data class Failed(val message: String) : PairingState
}

/**
 * Pure-Kotlin coroutine state machine driving the pairing handshake. Deliberately free
 * of Android framework imports so it runs (and is tested) on the JVM.
 *
 * Flow: [PairingState.Idle] -> [PairingState.Pairing] (POST /pair once) ->
 * [PairingState.WaitingApproval] (poll GET /status) -> [PairingState.Paired] on approval.
 * Expiry and any error response drive [PairingState.Failed] with a human-readable message.
 *
 * @param clock supplies "now" in epoch milliseconds; injected for deterministic tests.
 * @param pollIntervalMs delay between status polls (default 1s), advanced by virtual time in tests.
 */
class PairingController(
    private val client: ControlClient,
    private val phone: PhoneIdentity,
    private val rtp: RtpIdentity,
    private val clock: () -> Long = { System.currentTimeMillis() },
    private val pollIntervalMs: Long = 1000L,
) {
    private val _state = MutableStateFlow<PairingState>(PairingState.Idle)
    val state: StateFlow<PairingState> = _state.asStateFlow()

    /**
     * Run the handshake to a terminal state ([PairingState.Paired] or [PairingState.Failed]).
     * Suspends while polling; cancel the enclosing coroutine to abort.
     */
    suspend fun run(payload: PairingPayload) {
        if (isExpired(payload)) {
            _state.value = PairingState.Failed(EXPIRED_MESSAGE)
            return
        }

        _state.value = PairingState.Pairing
        when (val result = client.pair(payload, phone, rtp.sourcePort, rtp.ssrc)) {
            is PairResult.Failure -> {
                _state.value = PairingState.Failed(result.message)
                return
            }
            is PairResult.Accepted -> {
                if (result.approved) {
                    _state.value = PairingState.Paired(payload)
                    return
                }
            }
        }

        _state.value = PairingState.WaitingApproval
        while (true) {
            if (isExpired(payload)) {
                _state.value = PairingState.Failed(EXPIRED_MESSAGE)
                return
            }
            delay(pollIntervalMs)
            when (val result = client.status(payload)) {
                is StatusResult.Failure -> {
                    _state.value = PairingState.Failed(result.message)
                    return
                }
                is StatusResult.Ok -> {
                    if (result.approved) {
                        _state.value = PairingState.Paired(payload)
                        return
                    }
                }
            }
        }
    }

    private fun isExpired(payload: PairingPayload): Boolean {
        val expiresAt = try {
            OffsetDateTime.parse(payload.expires).toInstant().toEpochMilli()
        } catch (e: Exception) {
            // Unparseable expiry: don't block pairing on a formatting quirk.
            return false
        }
        return clock() >= expiresAt
    }

    companion object {
        const val EXPIRED_MESSAGE = "pairing code expired — rescan the QR"
    }
}
