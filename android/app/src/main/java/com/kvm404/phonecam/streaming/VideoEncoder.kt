package com.kvm404.phonecam.streaming

import android.media.Image
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.os.Bundle
import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.VideoProfile

/**
 * Thin, defensive MediaCodec (`video/avc`) wrapper. Android-framework code, so it is not
 * JVM-tested; the packetization it feeds is tested exhaustively in [RtpPacketizer].
 *
 * Input path (synchronous, called from the camera analyzer thread): [encode] copies a
 * [FrameData] into a codec input image and queues it, DROPPING the frame when no input
 * buffer is immediately available so the analyzer thread is never blocked.
 *
 * Output path (dedicated thread): each encoded buffer is turned into an Annex-B ByteArray;
 * codec-config buffers only cache SPS/PPS, normal buffers are packetized and sent. RTP
 * timestamps are `presentationTimeUs * 90 / 1000` (90 kHz).
 *
 * Errors are surfaced through [onError] so the UI can show them.
 */
class VideoEncoder(
    private val profile: VideoProfile,
    packetizer: RtpPacketizer,
    private val sender: RtpSender,
    bitRate: Int = BitrateController.targetFor(profile.width, profile.height, profile.fps),
    private val onError: (Throwable) -> Unit,
) {
    private var packetizer = packetizer
    private val rtpLock = Any()
    private var codec: MediaCodec? = null
    private var outputThread: Thread? = null

    /**
     * The color format the encoder actually accepted at [start], picked from
     * [COLOR_FORMAT_CANDIDATES]. Drives how [encode] fills the input buffer.
     */
    private var inputColorFormat = 0

    /** Frames seen since the last forced keyframe; interval comes from [bitrateController]. */
    private var framesSinceSyncRequest = 0

    private val lock = Any()
    private val bitrateController = BitrateController(capBps = bitRate)

    @Volatile
    private var running = false

    /**
     * Configure and start the encoder plus its output-draining thread.
     *
     * Devices disagree on BOTH the YUV input color format AND the config options their H.264
     * encoder accepts. The flexible format + CBR + baseline profile that works on the reference
     * vivo is rejected outright by some Unisoc encoders: the Samsung Galaxy A03's
     * OMX.sprd.h264.encoder supports NV12/I420 via DescribeColorFormat yet still fails
     * configure() with error -38 (OMX_ErrorUndefined) for every color format — the blocker is a
     * config OPTION (CBR bitrate mode and/or the baseline profile), not the color format. A
     * setInteger never throws, so those options can only be rejected at configure() time.
     *
     * So the whole configuration is NEGOTIATED as a matrix: for each color format (most
     * compatible first) try FULL_INTRA (CBR + baseline + intra-refresh ≈ 1 s), then FULL
     * (CBR + baseline, no intra-refresh), then MINIMAL (only the four mandatory keys).
     * The first combo that configures + starts wins; its color format drives the [encode]
     * packing branch. Devices that accept or ignore KEY_INTRA_REFRESH_PERIOD stay on
     * FULL_INTRA — intra-refresh replaces KEY_I_FRAME_INTERVAL, so recovery still depends
     * on [requestSyncFrame] / PARAMETER_KEY_REQUEST_SYNC_FRAME producing a real IDR. The
     * A03 falls through to the first combo that configures. If all combinations fail,
     * [onError] fires and no codec is left half-initialized.
     */
    fun start() {
        var lastError: Throwable? = null
        for (colorFormat in COLOR_FORMAT_CANDIDATES) {
            for (variant in FormatVariant.entries) {
                val c = try {
                    MediaCodec.createEncoderByType(MIME)
                } catch (t: Throwable) {
                    lastError = t
                    continue
                }
                try {
                    c.configure(
                        buildFormat(colorFormat, variant), null, null,
                        MediaCodec.CONFIGURE_FLAG_ENCODE,
                    )
                    c.start()
                    codec = c
                    inputColorFormat = colorFormat
                    running = true
                    outputThread =
                        Thread({ drainOutput(c) }, "phonecam-encoder-output").also { it.start() }
                    return
                } catch (t: Throwable) {
                    // This device rejected this color-format/options combo: drop the codec,
                    // try the next combo.
                    lastError = t
                    try {
                        c.release()
                    } catch (_: Throwable) {
                        // ignore
                    }
                }
            }
        }
        running = false
        codec = null
        val error = IllegalStateException(
            "no compatible H.264 encoder configuration on this device", lastError,
        )
        onError(error)
        throw error
    }

    /** Which optional config keys a [buildFormat] variant carries beyond the four mandatory keys. */
    private enum class FormatVariant {
        /** FULL plus intra-refresh; rejected extra keys fall through to [FULL]. */
        FULL_INTRA,

        /** The four mandatory keys plus CBR bitrate mode and the baseline profile. */
        FULL,

        /** ONLY the four mandatory keys: no bitrate mode, no profile, no level. */
        MINIMAL,
    }

    /**
     * Build the encoder [MediaFormat] for the given input [colorFormat] and [variant]. Every
     * variant sets the four mandatory keys (color format, bit rate, frame rate, i-frame
     * interval). [FormatVariant.FULL] adds CBR + baseline; [FormatVariant.FULL_INTRA] adds
     * those plus KEY_INTRA_REFRESH_PERIOD. [FormatVariant.MINIMAL] is keys only.
     */
    private fun buildFormat(colorFormat: Int, variant: FormatVariant): MediaFormat =
        MediaFormat.createVideoFormat(MIME, profile.width, profile.height).apply {
            setInteger(MediaFormat.KEY_COLOR_FORMAT, colorFormat)
            setInteger(MediaFormat.KEY_BIT_RATE, bitrateController.bitrate())
            setInteger(MediaFormat.KEY_FRAME_RATE, profile.fps)
            setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, I_FRAME_INTERVAL_SECONDS)
            if (variant == FormatVariant.FULL || variant == FormatVariant.FULL_INTRA) {
                // CBR keeps keyframes from spiking to many hundreds of KB (VBR keyframe bursts
                // fragmented into packet floods that dropped and froze whole GOPs); baseline
                // keeps decoding cheap. Vendor encoders that reject either fall to MINIMAL.
                setInteger(MediaFormat.KEY_BITRATE_MODE, CBR_MODE)
                setInteger(
                    MediaFormat.KEY_PROFILE,
                    MediaCodecInfo.CodecProfileLevel.AVCProfileBaseline,
                )
            }
            if (variant == FormatVariant.FULL_INTRA) {
                setInteger(MediaFormat.KEY_INTRA_REFRESH_PERIOD, profile.fps)
            }
        }

    /**
     * Copy [frame] into a free input buffer and queue it. If the codec has no input buffer
     * ready (queue full), the frame is dropped rather than blocking the analyzer thread.
     */
    fun encode(frame: FrameData) {
        val c = codec
        if (!running || c == null) return
        try {
            val skip = synchronized(lock) {
                framesSinceSyncRequest++
                bitrateController.shouldSkipEncode()
            }
            if (skip) return

            val index = c.dequeueInputBuffer(0)
            if (index < 0) {
                // queue full -> drop this frame; the controller may step bitrate down
                synchronized(lock) { bitrateController.onInputDrop() }
                return
            }

            val size = if (inputColorFormat ==
                MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420Flexible
            ) {
                // Flexible: stride-aware plane copy into the codec's input Image (the vivo path).
                val image = c.getInputImage(index)
                if (image == null) {
                    c.queueInputBuffer(index, 0, 0, frame.timestampUs, 0)
                    applyPendingParameters(c)
                    return
                }
                try {
                    fillImage(image, frame)
                } finally {
                    image.close()
                }
                c.getInputBuffer(index)?.capacity() ?: (frame.width * frame.height * 3 / 2)
            } else {
                // Semi-planar (NV12) / planar (I420): write a tightly-packed frame into the raw
                // input ByteBuffer at the configured width/height.
                val buffer = c.getInputBuffer(index)
                if (buffer == null) {
                    c.queueInputBuffer(index, 0, 0, frame.timestampUs, 0)
                    applyPendingParameters(c)
                    return
                }
                val packed = if (inputColorFormat ==
                    MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420SemiPlanar
                ) {
                    YuvPacking.packNv12(frame.y, frame.u, frame.v, frame.width, frame.height)
                } else {
                    YuvPacking.packI420(frame.y, frame.u, frame.v, frame.width, frame.height)
                }
                buffer.clear()
                buffer.put(packed)
                packed.size
            }
            c.queueInputBuffer(index, 0, size, frame.timestampUs, 0)
            applyPendingParameters(c)
        } catch (t: Throwable) {
            running = false
            onError(t)
        }
    }

    /**
     * Request an IDR on the next queued frame. Used after reconnect and as a
     * one-shot; the periodic counter is private.
     */
    fun requestSyncFrame() {
        synchronized(lock) {
            framesSinceSyncRequest = 0
            bitrateController.noteRequestKeyframe()
        }
    }

    /**
     * Receiver RTP age from GET /status `last_rtp_ms`. Every status read should
     * call this (even when the age is small) so the 1 s cadence can clear.
     * Applied on the next successful [encode] queue.
     */
    fun noteReceiverAge(lastRtpMs: Long) {
        synchronized(lock) { bitrateController.noteReceiverAge(lastRtpMs) }
    }

    /**
     * `/status.request_keyframe` one-shot. Call only together with
     * [noteReceiverAge] for that same status body — never this hook alone,
     * or the 1 s cadence has nothing to clear against.
     */
    fun noteRequestKeyframe() {
        synchronized(lock) { bitrateController.noteRequestKeyframe() }
    }

    /**
     * Swap SSRC / sequence after a socket recreate. Codec and canvas stay up.
     * Copies the SPS/PPS cache — MediaCodec will not emit another codec-config
     * on [requestSyncFrame], and a fresh packetizer would send a naked IDR.
     */
    fun replaceRtpIdentity(id: RtpIdentity) {
        val next = RtpPacketizer(id.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        synchronized(rtpLock) {
            next.copyParameterSetsFrom(packetizer)
            packetizer = next
        }
        requestSyncFrame()
    }

    fun sendFailures(): Int = sender.sendFailures()

    internal fun packetizeAccessUnit(annexB: ByteArray, timestamp90k: Long): List<ByteArray> =
        synchronized(rtpLock) { packetizer.packetize(annexB, timestamp90k) }

    /** Apply pending bitrate / sync only after a frame was queued. */
    private fun applyPendingParameters(c: MediaCodec) {
        val bitrate: Int?
        val forceSync: Boolean
        val periodic: Boolean
        val interval: Int
        synchronized(lock) {
            bitrate = if (bitrateController.consumeApplyBitrate()) {
                bitrateController.bitrate()
            } else {
                null
            }
            forceSync = bitrateController.consumeForceSync()
            interval = bitrateController.syncIntervalSeconds()
            periodic = framesSinceSyncRequest >= profile.fps * interval
            if (periodic) framesSinceSyncRequest = 0
        }
        if (!applyParameters(c, bitrate, forceSync || periodic)) {
            synchronized(lock) {
                if (bitrate != null) bitrateController.restoreApplyBitrate()
                if (forceSync) bitrateController.restoreForceSync()
                if (periodic) framesSinceSyncRequest = profile.fps * interval
            }
        }
    }

    private fun applyParameters(c: MediaCodec, bitrateBps: Int?, sync: Boolean): Boolean {
        if (bitrateBps == null && !sync) return true
        val params = Bundle()
        if (bitrateBps != null) {
            params.putInt(MediaCodec.PARAMETER_KEY_VIDEO_BITRATE, bitrateBps)
        }
        if (sync) {
            params.putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0)
        }
        return try {
            c.setParameters(params)
            true
        } catch (_: IllegalStateException) {
            false
        }
    }

    /** Stop the output thread and release the codec. Safe to call more than once. */
    fun stop() {
        running = false
        outputThread?.let { thread ->
            try {
                thread.join(STOP_JOIN_MS)
            } catch (_: InterruptedException) {
                Thread.currentThread().interrupt()
            }
        }
        outputThread = null
        try {
            codec?.stop()
        } catch (_: Throwable) {
            // ignore: already stopped / in error state
        }
        releaseCodecQuietly()
    }

    private fun drainOutput(c: MediaCodec) {
        val info = MediaCodec.BufferInfo()
        while (running) {
            val index = try {
                c.dequeueOutputBuffer(info, DEQUEUE_TIMEOUT_US)
            } catch (t: Throwable) {
                if (running) {
                    running = false
                    onError(t)
                }
                return
            }
            if (index == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                val newFormat = c.outputFormat
                val sps = newFormat.getByteBuffer("csd-0")
                val pps = newFormat.getByteBuffer("csd-1")
                if (sps != null && pps != null) {
                    val spsBytes = ByteArray(sps.remaining()).also { sps.get(it); sps.rewind() }
                    val ppsBytes = ByteArray(pps.remaining()).also { pps.get(it); pps.rewind() }
                    synchronized(rtpLock) { packetizer.cacheParameterSets(spsBytes, ppsBytes) }
                }
                continue
            }
            if (index < 0) continue // INFO_TRY_AGAIN_LATER / buffers changed

            try {
                var payload: ByteArray? = null
                var isConfig = false
                var rtpTimestamp = 0L

                try {
                    val buffer = c.getOutputBuffer(index)
                    if (buffer != null && info.size > 0) {
                        val data = ByteArray(info.size)
                        buffer.position(info.offset)
                        buffer.get(data, 0, info.size)
                        payload = data
                        isConfig = (info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0)
                        rtpTimestamp = info.presentationTimeUs * 90 / 1000
                    }
                } finally {
                    c.releaseOutputBuffer(index, false)
                }

                if (payload != null) {
                    if (isConfig) {
                        synchronized(rtpLock) { packetizer.cacheParameterSets(payload) }
                    } else {
                        val packets = packetizeAccessUnit(payload, rtpTimestamp)
                        try {
                            sender.send(packets)
                        } catch (_: Throwable) {
                            // send() must not tear down the encoder on UDP loss
                        }
                    }
                }
            } catch (t: Throwable) {
                if (running) {
                    running = false
                    onError(t)
                }
                return
            }
        }
    }

    private fun fillImage(image: Image, frame: FrameData) {
        val planes = image.planes
        fillPlane(planes[0], frame.y, frame.width, frame.height)
        fillPlane(planes[1], frame.u, frame.width / 2, frame.height / 2)
        fillPlane(planes[2], frame.v, frame.width / 2, frame.height / 2)
    }

    private fun fillPlane(plane: Image.Plane, src: ByteArray, width: Int, height: Int) {
        val buffer = plane.buffer
        val rowStride = plane.rowStride
        val pixelStride = plane.pixelStride
        var srcPos = 0
        for (row in 0 until height) {
            val rowStart = row * rowStride
            if (pixelStride == 1) {
                buffer.position(rowStart)
                buffer.put(src, srcPos, width)
                srcPos += width
            } else {
                for (col in 0 until width) {
                    buffer.put(rowStart + col * pixelStride, src[srcPos++])
                }
            }
        }
    }

    private fun releaseCodecQuietly() {
        try {
            codec?.release()
        } catch (_: Throwable) {
            // ignore
        }
        codec = null
    }

    companion object {
        /**
         * Encoder input color formats to try at [start], most compatible first. Flexible is the
         * historically-working format; the semi-planar/planar fallbacks cover vendor encoders
         * (e.g. Unisoc) that reject it.
         */
        private val COLOR_FORMAT_CANDIDATES = intArrayOf(
            MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420Flexible,
            MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420SemiPlanar,
            MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420Planar,
        )

        private const val MIME = "video/avc"
        private const val I_FRAME_INTERVAL_SECONDS = 1
        private const val CBR_MODE = MediaCodecInfo.EncoderCapabilities.BITRATE_MODE_CBR
        private const val DEQUEUE_TIMEOUT_US = 10_000L
        private const val STOP_JOIN_MS = 500L
    }
}
