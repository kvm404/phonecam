package com.kvm404.phonecam.streaming

import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetSocketAddress

/**
 * Sends already-built RTP packets to the receiver. Kept behind an interface so the encoder
 * output path can be exercised against a fake in tests.
 */
interface RtpSender {
    /** Send each packet in order. Implementations must not throw on I/O failure. */
    fun send(packets: List<ByteArray>)

    /** Count of failed datagrams since construction; default senders report 0. */
    fun sendFailures(): Int = 0
}

/**
 * Real [RtpSender] that writes UDP datagrams from the pairing-committed socket, preserving
 * the source port the Linux receiver pinned during pairing.
 *
 * Sends are PACED: a keyframe fragments into hundreds of packets, and blasting them
 * back-to-back overflows the Wi-Fi driver queue and the receiver's UDP buffer, losing
 * fragments — which makes the whole keyframe (and every frame until the next one)
 * undecodable, freezing the stream for a full GOP. A ~1ms pause every [PACING_BURST]
 * packets spreads a large keyframe over a few tens of milliseconds, which is invisible
 * next to the jitter buffer but eliminates the burst loss.
 *
 * @param socket the OPEN socket owned by the pairing `RtpIdentity` (do not close it here).
 * @param target the receiver's RTP host:port from the pairing payload.
 */
class UdpRtpSender(
    socket: DatagramSocket,
    target: InetSocketAddress,
    private val pauseNanos: Long = PACING_PAUSE_NANOS,
) : RtpSender {
    private var socket = socket
    private var target = target

    @Volatile
    private var failures = 0

    @Synchronized
    fun setSocket(socket: DatagramSocket) {
        this.socket = socket
    }

    @Synchronized
    fun setTarget(target: InetSocketAddress) {
        this.target = target
    }

    override fun sendFailures(): Int = failures

    @Synchronized
    override fun send(packets: List<ByteArray>) {
        val sock = socket
        val dest = target
        for ((index, packet) in packets.withIndex()) {
            if (index != 0 && index % PACING_BURST == 0 && pauseNanos > 0) {
                java.util.concurrent.locks.LockSupport.parkNanos(pauseNanos)
            }
            try {
                sock.send(DatagramPacket(packet, packet.size, dest))
            } catch (_: Exception) {
                failures++
            }
        }
    }

    companion object {
        /** Packets sent back-to-back before pausing. */
        const val PACING_BURST = 8

        /** Pause between bursts (~1ms): a 300-packet keyframe spreads over ~37ms. */
        const val PACING_PAUSE_NANOS = 1_000_000L
    }
}
