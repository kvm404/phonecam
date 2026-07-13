package com.kvm404.phonecam.pairing

import java.net.DatagramSocket
import java.security.SecureRandom

/**
 * The RTP identity the phone commits to during pairing: a non-zero SSRC and the UDP
 * source port it will later stream FROM.
 *
 * The bound [socket] is opened once at [create] time and kept OPEN so the streamer can
 * send from the exact same source port that was announced during pairing (the Linux
 * receiver pins the approved source IP/port/SSRC and drops anything else). Ownership of
 * the socket transfers to the caller, which must [close] it when streaming ends.
 */
class RtpIdentity(
    val ssrc: Long,
    val sourcePort: Int,
    /** Open UDP socket bound to [sourcePort]; null for identities built in tests. */
    val socket: DatagramSocket? = null,
) {
    /** Release the bound socket, if any. Idempotent. */
    fun close() {
        socket?.close()
    }

    companion object {
        private val random = SecureRandom()

        /** Random non-zero unsigned 32-bit SSRC as required by the pairing protocol. */
        fun randomSsrc(): Long {
            var value = 0L
            while (value == 0L) {
                value = random.nextLong() and 0xFFFFFFFFL
            }
            return value
        }

        /**
         * Pick an ephemeral UDP source port by binding port 0, reading the assigned
         * local port and releasing the socket immediately. Retained as a lightweight
         * helper (and for tests); the live streaming path uses [create], which keeps the
         * socket open.
         */
        fun pickSourcePort(): Int =
            DatagramSocket(0).use { it.localPort }

        /**
         * Bind an ephemeral UDP socket ONCE and keep it open. The assigned local port is
         * recorded as [sourcePort] and announced during pairing; the same socket is later
         * handed to the streamer so RTP leaves from that exact port.
         */
        fun create(): RtpIdentity {
            val socket = DatagramSocket(0)
            return RtpIdentity(randomSsrc(), socket.localPort, socket)
        }
    }
}
