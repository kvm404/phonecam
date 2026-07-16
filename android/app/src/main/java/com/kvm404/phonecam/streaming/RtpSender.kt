package com.kvm404.phonecam.streaming

import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetSocketAddress

/**
 * Sends already-built RTP packets to the receiver. Kept behind an interface so the encoder
 * output path can be exercised against a fake in tests.
 */
interface RtpSender {
    /** Send each packet in order. May throw on I/O failure. */
    fun send(packets: List<ByteArray>)
}

/**
 * Real [RtpSender] that writes UDP datagrams from the pairing-committed socket, preserving
 * the source port the Linux receiver pinned during pairing.
 *
 * @param socket the OPEN socket owned by the pairing `RtpIdentity` (do not close it here).
 * @param target the receiver's RTP host:port from the pairing payload.
 */
class UdpRtpSender(
    private val socket: DatagramSocket,
    private val target: InetSocketAddress,
) : RtpSender {
    override fun send(packets: List<ByteArray>) {
        for (packet in packets) {
            socket.send(DatagramPacket(packet, packet.size, target))
        }
    }
}
