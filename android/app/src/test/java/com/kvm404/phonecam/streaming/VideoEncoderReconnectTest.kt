package com.kvm404.phonecam.streaming

import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.VideoProfile
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * JVM coverage for [VideoEncoder.replaceRtpIdentity]: the next IDR after an
 * SSRC swap must still emit cached SPS+PPS. MediaCodec is not started.
 */
class VideoEncoderReconnectTest {

    private class CollectingSender : RtpSender {
        override fun send(packets: List<ByteArray>) {}
    }

    private fun annexB4(vararg nal: Int): ByteArray =
        byteArrayOf(0, 0, 0, 1) + nal.map { it.toByte() }.toByteArray()

    private fun ssrcOf(packet: ByteArray): Long =
        ((packet[8].toLong() and 0xFF) shl 24) or
            ((packet[9].toLong() and 0xFF) shl 16) or
            ((packet[10].toLong() and 0xFF) shl 8) or
            (packet[11].toLong() and 0xFF)

    private fun nalType(packet: ByteArray): Int = packet[12].toInt() and 0x1F

    @Test
    fun `replaceRtpIdentity keeps SPS PPS on the next IDR`() {
        val oldSsrc = 0x11111111L
        val newSsrc = 0x22222222L
        val packetizer = RtpPacketizer(oldSsrc, initialSequenceNumber = 10)
        packetizer.cacheParameterSets(
            annexB4(0x67, 0x01, 0x02) + annexB4(0x68, 0x03),
        )
        val encoder = VideoEncoder(
            VideoProfile(1280, 720, 30),
            packetizer,
            CollectingSender(),
        ) { }

        encoder.replaceRtpIdentity(RtpIdentity(ssrc = newSsrc, sourcePort = 40001))

        val packets = encoder.packetizeAccessUnit(annexB4(0x65, 0xAA, 0xBB), 900L)
        assertEquals(3, packets.size)
        assertEquals(7, nalType(packets[0]))
        assertEquals(8, nalType(packets[1]))
        assertEquals(5, nalType(packets[2]))
        packets.forEach { packet ->
            assertEquals(newSsrc, ssrcOf(packet))
            assertTrue(packet.size > RtpPacketizer.RTP_HEADER_SIZE)
        }
        // SPS/PPS stay single-NAL (tiny parameter sets).
        assertEquals(RtpPacketizer.RTP_HEADER_SIZE + 3, packets[0].size)
        assertEquals(RtpPacketizer.RTP_HEADER_SIZE + 2, packets[1].size)
    }
}
