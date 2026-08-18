package com.kvm404.phonecam.streaming

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetSocketAddress

class UdpRtpSenderTest {

    @Test
    fun `send on a closed socket increments failures and does not throw`() {
        val socket = DatagramSocket()
        socket.close()
        val sender = UdpRtpSender(socket, InetSocketAddress("127.0.0.1", 9), pauseNanos = 0)
        sender.send(listOf(byteArrayOf(1, 2, 3), byteArrayOf(4, 5)))
        assertEquals(2, sender.sendFailures())
    }

    @Test
    fun `setSocket and setTarget apply to the next send`() {
        val receiver = DatagramSocket(0)
        receiver.soTimeout = 1000
        val discarded = DatagramSocket()
        val live = DatagramSocket()
        val sender = UdpRtpSender(
            discarded,
            InetSocketAddress("127.0.0.1", 1),
            pauseNanos = 0,
        )
        sender.setSocket(live)
        sender.setTarget(InetSocketAddress("127.0.0.1", receiver.localPort))
        val payload = byteArrayOf(9, 8, 7, 6)
        sender.send(listOf(payload))
        val buf = ByteArray(16)
        val packet = DatagramPacket(buf, buf.size)
        receiver.receive(packet)
        assertArrayEquals(payload, buf.copyOf(packet.length))
        assertEquals(0, sender.sendFailures())
        discarded.close()
        live.close()
        receiver.close()
    }

    @Test
    fun `sendFailures stays zero when datagrams are accepted`() {
        val receiver = DatagramSocket(0)
        val socket = DatagramSocket()
        val sender = UdpRtpSender(
            socket,
            InetSocketAddress("127.0.0.1", receiver.localPort),
            pauseNanos = 0,
        )
        sender.send(listOf(byteArrayOf(1)))
        assertTrue(sender.sendFailures() == 0)
        socket.close()
        receiver.close()
    }
}
