package com.kvm404.phonecam.pairing

import java.net.DatagramSocket
import java.security.SecureRandom

/**
 * The RTP identity the phone commits to during pairing: a non-zero SSRC and the UDP
 * source port it will later stream FROM.
 */
data class RtpIdentity(
    val ssrc: Long,
    val sourcePort: Int,
) {
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
         * local port and releasing the socket. The streamer rebinds this port later.
         */
        fun pickSourcePort(): Int =
            DatagramSocket(0).use { it.localPort }

        fun create(): RtpIdentity = RtpIdentity(randomSsrc(), pickSourcePort())
    }
}
