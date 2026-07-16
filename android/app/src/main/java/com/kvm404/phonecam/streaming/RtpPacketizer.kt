package com.kvm404.phonecam.streaming

import java.security.SecureRandom

/**
 * Pure-Kotlin RTP/H.264 packetizer implementing RFC 6184 packetization-mode 1.
 *
 * Deliberately free of any Android framework types so it runs and is exhaustively tested on
 * the plain JVM. Callers feed encoded access units in Annex-B format (start codes
 * 0x000001 / 0x00000001, possibly several NAL units concatenated) together with a 90 kHz
 * RTP timestamp; the packetizer returns the RTP packets to put on the wire.
 *
 * Responsibilities:
 *  - Split Annex-B into NAL units (3- and 4-byte start codes).
 *  - Cache SPS (type 7) / PPS (type 8) whenever seen, including from a codec-config buffer.
 *  - Re-emit the cached SPS and PPS (as single-NAL packets, same timestamp) immediately
 *    before each IDR (type 5) so a mid-stream joiner can decode.
 *  - Emit NAL units <= [maxPayload] as single-NAL-unit packets and fragment larger ones
 *    into FU-A packets.
 *  - Set the RTP marker bit on the last packet of an access unit only.
 *
 * Not thread-safe: call from a single (encoder output) thread.
 *
 * @param ssrc the 32-bit SSRC committed during pairing (stored in every RTP header).
 * @param initialSequenceNumber the starting RTP sequence number; RANDOM in production
 *   (see [randomInitialSequenceNumber]) and injected for deterministic tests.
 * @param payloadType RTP dynamic payload type; 96 to match the receiver's caps.
 * @param maxPayload maximum RTP payload size (excludes the 12-byte header). NALs larger
 *   than this are fragmented with FU-A.
 */
class RtpPacketizer(
    private val ssrc: Long,
    initialSequenceNumber: Int,
    private val payloadType: Int = DEFAULT_PAYLOAD_TYPE,
    private val maxPayload: Int = DEFAULT_MAX_PAYLOAD,
) {
    /** Current RTP sequence number, wrapping at 65535 -> 0. */
    private var sequenceNumber: Int = initialSequenceNumber and 0xFFFF

    private var cachedSps: ByteArray? = null
    private var cachedPps: ByteArray? = null

    /**
     * Cache the parameter sets carried by a MediaCodec codec-config buffer WITHOUT emitting
     * any RTP packets. The SPS/PPS are re-emitted later, ahead of the next IDR.
     */
    fun cacheParameterSets(annexB: ByteArray) {
        for (nal in splitAnnexB(annexB)) {
            when (nalType(nal)) {
                NAL_TYPE_SPS -> cachedSps = nal
                NAL_TYPE_PPS -> cachedPps = nal
            }
        }
    }

    /**
     * Packetize one access unit into RTP packets.
     *
     * @param annexB the encoded access unit (Annex-B, one or more NAL units).
     * @param timestamp90k the RTP timestamp (90 kHz) shared by every packet of this AU.
     */
    fun packetize(annexB: ByteArray, timestamp90k: Long): List<ByteArray> {
        val nals = splitAnnexB(annexB)

        // Resolve the ordered list of NAL units to actually transmit, injecting cached
        // SPS/PPS ahead of an IDR when they are not already present inline in this AU.
        val toSend = ArrayList<ByteArray>(nals.size + 2)
        var spsPresent = false
        var ppsPresent = false
        for (nal in nals) {
            when (nalType(nal)) {
                NAL_TYPE_SPS -> {
                    cachedSps = nal
                    spsPresent = true
                    toSend.add(nal)
                }
                NAL_TYPE_PPS -> {
                    cachedPps = nal
                    ppsPresent = true
                    toSend.add(nal)
                }
                NAL_TYPE_IDR -> {
                    if (!spsPresent) cachedSps?.let { toSend.add(it); spsPresent = true }
                    if (!ppsPresent) cachedPps?.let { toSend.add(it); ppsPresent = true }
                    toSend.add(nal)
                }
                else -> toSend.add(nal)
            }
        }

        val packets = ArrayList<ByteArray>()
        for (nal in toSend) {
            if (nal.size <= maxPayload) {
                packets.add(buildSingleNalPacket(nal, timestamp90k))
            } else {
                buildFuAPackets(nal, timestamp90k, packets)
            }
        }

        // Marker bit marks the end of the access unit: set it on the final packet only.
        if (packets.isNotEmpty()) {
            val last = packets[packets.size - 1]
            last[1] = (last[1].toInt() or 0x80).toByte()
        }
        return packets
    }

    private fun buildSingleNalPacket(nal: ByteArray, timestamp90k: Long): ByteArray {
        val packet = ByteArray(RTP_HEADER_SIZE + nal.size)
        writeHeader(packet, timestamp90k)
        System.arraycopy(nal, 0, packet, RTP_HEADER_SIZE, nal.size)
        return packet
    }

    private fun buildFuAPackets(nal: ByteArray, timestamp90k: Long, out: MutableList<ByteArray>) {
        val nalHeader = nal[0].toInt() and 0xFF
        val fuIndicator = (nalHeader and 0x60) or FU_A_TYPE // keep F+NRI, type = 28
        val originalType = nalHeader and 0x1F
        val maxFragment = maxPayload - FU_A_HEADER_SIZE

        var offset = 1 // skip the original 1-byte NAL header
        val total = nal.size
        while (offset < total) {
            val remaining = total - offset
            val fragLen = if (remaining < maxFragment) remaining else maxFragment
            val start = offset == 1
            val end = offset + fragLen >= total

            var fuHeader = originalType
            if (start) fuHeader = fuHeader or 0x80 // S bit
            if (end) fuHeader = fuHeader or 0x40 // E bit

            val packet = ByteArray(RTP_HEADER_SIZE + FU_A_HEADER_SIZE + fragLen)
            writeHeader(packet, timestamp90k)
            packet[RTP_HEADER_SIZE] = fuIndicator.toByte()
            packet[RTP_HEADER_SIZE + 1] = fuHeader.toByte()
            System.arraycopy(nal, offset, packet, RTP_HEADER_SIZE + FU_A_HEADER_SIZE, fragLen)
            out.add(packet)

            offset += fragLen
        }
    }

    /**
     * Write a 12-byte RTP header (V=2, no padding/extension/CSRC, marker=0) and advance the
     * sequence number. The marker bit is stamped on the final packet by [packetize].
     */
    private fun writeHeader(packet: ByteArray, timestamp90k: Long) {
        packet[0] = 0x80.toByte() // V=2, P=0, X=0, CC=0
        packet[1] = (payloadType and 0x7F).toByte() // M=0, PT

        val seq = sequenceNumber
        packet[2] = (seq ushr 8).toByte()
        packet[3] = seq.toByte()

        val ts = timestamp90k.toInt()
        packet[4] = (ts ushr 24).toByte()
        packet[5] = (ts ushr 16).toByte()
        packet[6] = (ts ushr 8).toByte()
        packet[7] = ts.toByte()

        packet[8] = (ssrc ushr 24).toByte()
        packet[9] = (ssrc ushr 16).toByte()
        packet[10] = (ssrc ushr 8).toByte()
        packet[11] = ssrc.toByte()

        sequenceNumber = (sequenceNumber + 1) and 0xFFFF
    }

    companion object {
        const val DEFAULT_PAYLOAD_TYPE = 96
        const val DEFAULT_MAX_PAYLOAD = 1200
        const val RTP_HEADER_SIZE = 12
        const val FU_A_HEADER_SIZE = 2
        const val FU_A_TYPE = 28

        const val NAL_TYPE_IDR = 5
        const val NAL_TYPE_SPS = 7
        const val NAL_TYPE_PPS = 8

        /** A random initial RTP sequence number in [0, 65535], per RFC 3550. */
        fun randomInitialSequenceNumber(): Int = SecureRandom().nextInt(0x1_0000)

        private fun nalType(nal: ByteArray): Int =
            if (nal.isEmpty()) -1 else nal[0].toInt() and 0x1F

        /**
         * Split an Annex-B buffer into NAL units. Handles both 3-byte (0x000001) and
         * 4-byte (0x00000001) start codes; each returned array starts at the NAL header
         * byte and runs up to the next start code.
         */
        fun splitAnnexB(data: ByteArray): List<ByteArray> {
            val codeStarts = ArrayList<Int>() // index where each start code begins
            val nalStarts = ArrayList<Int>() // index of each NAL's first (header) byte

            var i = 0
            while (i + 2 < data.size) {
                if (data[i].toInt() == 0 && data[i + 1].toInt() == 0 && data[i + 2].toInt() == 1) {
                    val codeBegin = if (i > 0 && data[i - 1].toInt() == 0) i - 1 else i
                    codeStarts.add(codeBegin)
                    nalStarts.add(i + 3)
                    i += 3
                } else {
                    i++
                }
            }

            val result = ArrayList<ByteArray>(nalStarts.size)
            for (k in nalStarts.indices) {
                val begin = nalStarts[k]
                val end = if (k + 1 < codeStarts.size) codeStarts[k + 1] else data.size
                if (end > begin) {
                    result.add(data.copyOfRange(begin, end))
                }
            }
            return result
        }
    }
}
