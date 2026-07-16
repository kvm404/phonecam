package com.kvm404.phonecam.streaming

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Exhaustive JVM tests for the pure RTP/H.264 packetizer. No Android types involved.
 */
class RtpPacketizerTest {

    private val ssrc = 0x11223344L
    private val initialSeq = 1000

    private fun packetizer(maxPayload: Int = RtpPacketizer.DEFAULT_MAX_PAYLOAD) =
        RtpPacketizer(ssrc, initialSeq, maxPayload = maxPayload)

    /** Prefix a NAL body with a 4-byte start code. */
    private fun annexB4(vararg nal: Int): ByteArray =
        byteArrayOf(0, 0, 0, 1) + nal.map { it.toByte() }.toByteArray()

    private fun annexB3(vararg nal: Int): ByteArray =
        byteArrayOf(0, 0, 1) + nal.map { it.toByte() }.toByteArray()

    private fun seqOf(packet: ByteArray): Int =
        ((packet[2].toInt() and 0xFF) shl 8) or (packet[3].toInt() and 0xFF)

    private fun tsOf(packet: ByteArray): Long =
        ((packet[4].toLong() and 0xFF) shl 24) or
            ((packet[5].toLong() and 0xFF) shl 16) or
            ((packet[6].toLong() and 0xFF) shl 8) or
            (packet[7].toLong() and 0xFF)

    private fun ssrcOf(packet: ByteArray): Long =
        ((packet[8].toLong() and 0xFF) shl 24) or
            ((packet[9].toLong() and 0xFF) shl 16) or
            ((packet[10].toLong() and 0xFF) shl 8) or
            (packet[11].toLong() and 0xFF)

    private fun markerSet(packet: ByteArray): Boolean = (packet[1].toInt() and 0x80) != 0

    private fun payloadType(packet: ByteArray): Int = packet[1].toInt() and 0x7F

    @Test
    fun `single NAL packet has correct header fields and payload`() {
        // NAL: header byte 0x41 (type 1, non-IDR slice) + body.
        val nal = intArrayOf(0x41, 0xAA, 0xBB, 0xCC)
        val packets = packetizer().packetize(annexB4(*nal), timestamp90k = 0x0000_9000L)

        assertEquals(1, packets.size)
        val p = packets[0]
        // Version=2, no padding/extension/CSRC.
        assertEquals(0x80.toByte(), p[0])
        assertEquals(96, payloadType(p))
        assertEquals(initialSeq, seqOf(p))
        assertEquals(0x9000L, tsOf(p))
        assertEquals(ssrc, ssrcOf(p))
        // Marker set on the last (only) packet of the AU.
        assertTrue(markerSet(p))
        // Payload is the whole NAL (header byte included).
        val payload = p.copyOfRange(12, p.size)
        assertArrayEquals(byteArrayOf(0x41, 0xAA.toByte(), 0xBB.toByte(), 0xCC.toByte()), payload)
    }

    @Test
    fun `three and four byte start codes both parse`() {
        val p3 = packetizer().packetize(annexB3(0x41, 0x01, 0x02), 90L)
        val p4 = packetizer().packetize(annexB4(0x41, 0x01, 0x02), 90L)
        assertEquals(1, p3.size)
        assertEquals(1, p4.size)
        assertArrayEquals(p3[0].copyOfRange(12, p3[0].size), p4[0].copyOfRange(12, p4[0].size))
    }

    @Test
    fun `multiple NALs in one access unit produce sequential packets, marker only on last`() {
        // Two non-IDR slices concatenated in one AU.
        val au = annexB4(0x41, 0x01) + annexB4(0x41, 0x02)
        val packets = packetizer().packetize(au, 500L)

        assertEquals(2, packets.size)
        assertEquals(initialSeq, seqOf(packets[0]))
        assertEquals(initialSeq + 1, seqOf(packets[1]))
        assertFalse(markerSet(packets[0]))
        assertTrue(markerSet(packets[1]))
        // Same timestamp across the AU.
        assertEquals(500L, tsOf(packets[0]))
        assertEquals(500L, tsOf(packets[1]))
    }

    @Test
    fun `large NAL is fragmented into FU-A packets`() {
        val maxPayload = 20
        // NAL header 0x65 = F=0, NRI=3 (0x60), type=5 (IDR). Body of 50 bytes.
        val body = IntArray(50) { (it and 0xFF) }
        val nalInts = intArrayOf(0x65) + body
        val packetizer = packetizer(maxPayload = maxPayload)
        // Pre-cache SPS/PPS so the IDR path does not inject extra packets here.
        // (We test injection separately; supply none so nothing is prepended.)
        val packets = packetizer.packetize(annexB4(*nalInts), 700L)

        // Each FU-A payload carries at most maxPayload - 2 body bytes.
        val fragmentCapacity = maxPayload - 2
        val bodyLen = nalInts.size - 1 // exclude original NAL header
        val expectedFragments = (bodyLen + fragmentCapacity - 1) / fragmentCapacity
        assertEquals(expectedFragments, packets.size)

        val fuIndicatorExpected = (0x65 and 0x60) or 28 // 0x60 | 28 = 0x7C
        val reassembled = ArrayList<Byte>()
        packets.forEachIndexed { index, p ->
            val fuIndicator = p[12].toInt() and 0xFF
            val fuHeader = p[13].toInt() and 0xFF
            assertEquals(fuIndicatorExpected, fuIndicator)
            // Type preserved in FU header low 5 bits.
            assertEquals(5, fuHeader and 0x1F)
            val isFirst = index == 0
            val isLast = index == packets.size - 1
            assertEquals("S bit", isFirst, (fuHeader and 0x80) != 0)
            assertEquals("E bit", isLast, (fuHeader and 0x40) != 0)
            // Marker only on the final packet.
            assertEquals(isLast, markerSet(p))
            // Sequence increments.
            assertEquals(initialSeq + index, seqOf(p))
            reassembled.addAll(p.copyOfRange(14, p.size).toList())
        }
        // Reassembled fragments equal the original NAL minus its header byte.
        assertArrayEquals(
            body.map { it.toByte() }.toByteArray(),
            reassembled.toByteArray(),
        )
    }

    @Test
    fun `SPS and PPS are cached and re-emitted before IDR with same timestamp`() {
        val p = packetizer()
        // Feed an AU containing SPS (type 7), PPS (type 8) inline, then a later IDR-only AU.
        val sps = intArrayOf(0x67, 0x01, 0x02)
        val pps = intArrayOf(0x68, 0x03)
        p.packetize(annexB4(*sps) + annexB4(*pps), 100L)

        // Now an IDR-only AU: cached SPS/PPS must be prepended, all sharing the IDR timestamp.
        val idr = intArrayOf(0x65, 0xAA, 0xBB)
        val packets = p.packetize(annexB4(*idr), 200L)

        assertEquals(3, packets.size)
        // Order: SPS, PPS, IDR.
        assertEquals(7, packets[0][12].toInt() and 0x1F)
        assertEquals(8, packets[1][12].toInt() and 0x1F)
        assertEquals(5, packets[2][12].toInt() and 0x1F)
        // All share the IDR's timestamp.
        packets.forEach { assertEquals(200L, tsOf(it)) }
        // Marker only on the last (IDR) packet.
        assertFalse(markerSet(packets[0]))
        assertFalse(markerSet(packets[1]))
        assertTrue(markerSet(packets[2]))
    }

    @Test
    fun `inline SPS PPS with IDR are not duplicated`() {
        val p = packetizer()
        val sps = intArrayOf(0x67, 0x01)
        val pps = intArrayOf(0x68, 0x02)
        val idr = intArrayOf(0x65, 0x03)
        val au = annexB4(*sps) + annexB4(*pps) + annexB4(*idr)
        val packets = p.packetize(au, 300L)
        // Exactly three packets, no re-injected duplicates.
        assertEquals(3, packets.size)
        assertEquals(7, packets[0][12].toInt() and 0x1F)
        assertEquals(8, packets[1][12].toInt() and 0x1F)
        assertEquals(5, packets[2][12].toInt() and 0x1F)
    }

    @Test
    fun `config buffer caches parameter sets without emitting`() {
        val p = packetizer()
        val sps = intArrayOf(0x67, 0x01, 0x02)
        val pps = intArrayOf(0x68, 0x03)
        // cacheParameterSets returns Unit and must not consume sequence numbers.
        p.cacheParameterSets(annexB4(*sps) + annexB4(*pps))

        val idr = intArrayOf(0x65, 0x09)
        val packets = p.packetize(annexB4(*idr), 400L)
        assertEquals(3, packets.size)
        // First emitted packet still uses the initial sequence number (nothing consumed by caching).
        assertEquals(initialSeq, seqOf(packets[0]))
        assertEquals(7, packets[0][12].toInt() and 0x1F)
        assertEquals(8, packets[1][12].toInt() and 0x1F)
    }

    @Test
    fun `sequence number wraps at 65535 to 0`() {
        val p = RtpPacketizer(ssrc, initialSequenceNumber = 65535)
        val first = p.packetize(annexB4(0x41, 0x00), 1L)
        val second = p.packetize(annexB4(0x41, 0x01), 2L)
        assertEquals(65535, seqOf(first[0]))
        assertEquals(0, seqOf(second[0]))
    }

    @Test
    fun `timestamp truncates to low 32 bits big-endian`() {
        // 0x1_0000_0001 -> low 32 bits = 1.
        val packets = packetizer().packetize(annexB4(0x41, 0x00), 0x1_0000_0001L)
        assertEquals(1L, tsOf(packets[0]))
    }

    @Test
    fun `splitAnnexB handles mixed start code lengths`() {
        val data = byteArrayOf(0, 0, 0, 1, 0x41, 0x0A) + byteArrayOf(0, 0, 1, 0x41, 0x0B)
        val nals = RtpPacketizer.splitAnnexB(data)
        assertEquals(2, nals.size)
        assertArrayEquals(byteArrayOf(0x41, 0x0A), nals[0])
        assertArrayEquals(byteArrayOf(0x41, 0x0B), nals[1])
    }
}
