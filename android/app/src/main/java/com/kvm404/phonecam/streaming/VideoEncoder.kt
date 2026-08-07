package com.kvm404.phonecam.streaming

import android.media.Image
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.os.Bundle
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
    private val packetizer: RtpPacketizer,
    private val sender: RtpSender,
    private val bitRate: Int = DEFAULT_BIT_RATE,
    private val onError: (Throwable) -> Unit,
) {
    private var codec: MediaCodec? = null
    private var outputThread: Thread? = null

    /**
     * The color format the encoder actually accepted at [start], picked from
     * [COLOR_FORMAT_CANDIDATES]. Drives how [encode] fills the input buffer.
     */
    private var inputColorFormat = 0

    /** Frames queued since the last forced keyframe; see the sync request in [encode]. */
    private var framesSinceSyncRequest = 0

    @Volatile
    private var running = false

    /**
     * Configure and start the encoder plus its output-draining thread.
     *
     * Devices disagree on which YUV input color format their H.264 encoder accepts — the
     * flexible format that works on the reference vivo is rejected outright by some Unisoc
     * encoders (e.g. the Samsung Galaxy A03's OMX.sprd.h264.encoder returns configure error
     * -38). So the format is NEGOTIATED: each candidate in [COLOR_FORMAT_CANDIDATES], most
     * compatible first, is tried with a fresh codec until one configures + starts. If none do,
     * [onError] fires with a clear message and no codec is left half-initialized.
     */
    fun start() {
        var lastError: Throwable? = null
        for (colorFormat in COLOR_FORMAT_CANDIDATES) {
            val c = try {
                MediaCodec.createEncoderByType(MIME)
            } catch (t: Throwable) {
                lastError = t
                continue
            }
            try {
                c.configure(
                    buildFormat(colorFormat), null, null, MediaCodec.CONFIGURE_FLAG_ENCODE,
                )
                c.start()
                codec = c
                inputColorFormat = colorFormat
                running = true
                outputThread =
                    Thread({ drainOutput(c) }, "phonecam-encoder-output").also { it.start() }
                return
            } catch (t: Throwable) {
                // This device rejected this color format: drop this codec and try the next.
                lastError = t
                try {
                    c.release()
                } catch (_: Throwable) {
                    // ignore
                }
            }
        }
        running = false
        codec = null
        onError(
            IllegalStateException(
                "no compatible H.264 color format on this device", lastError,
            ),
        )
    }

    /** Build the encoder [MediaFormat] with the given input [colorFormat]. */
    private fun buildFormat(colorFormat: Int): MediaFormat =
        MediaFormat.createVideoFormat(MIME, profile.width, profile.height).apply {
            setInteger(MediaFormat.KEY_COLOR_FORMAT, colorFormat)
            setInteger(MediaFormat.KEY_BIT_RATE, bitRate)
            setInteger(MediaFormat.KEY_FRAME_RATE, profile.fps)
            setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, I_FRAME_INTERVAL_SECONDS)
            // CBR keeps keyframes from spiking to many hundreds of KB (VBR
            // keyframe bursts were fragmenting into packet floods that dropped
            // and froze whole GOPs). Best-effort: not every device honours it.
            trySet { setInteger(MediaFormat.KEY_BITRATE_MODE, CBR_MODE) }
            trySet {
                setInteger(
                    MediaFormat.KEY_PROFILE,
                    MediaCodecInfo.CodecProfileLevel.AVCProfileBaseline,
                )
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
            val index = c.dequeueInputBuffer(0)
            if (index < 0) return // queue full -> drop this frame

            if (inputColorFormat ==
                MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420Flexible
            ) {
                // Flexible: stride-aware plane copy into the codec's input Image (the vivo path).
                val image = c.getInputImage(index)
                if (image == null) {
                    c.queueInputBuffer(index, 0, 0, frame.timestampUs, 0)
                    return
                }
                fillImage(image, frame)
            } else {
                // Semi-planar (NV12) / planar (I420): write a tightly-packed frame into the raw
                // input ByteBuffer at the configured width/height.
                val buffer = c.getInputBuffer(index)
                if (buffer == null) {
                    c.queueInputBuffer(index, 0, 0, frame.timestampUs, 0)
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
            }
            val size = frame.width * frame.height * 3 / 2
            c.queueInputBuffer(index, 0, size, frame.timestampUs, 0)

            // Vendor encoders don't reliably honour KEY_I_FRAME_INTERVAL, and after
            // RTP packet loss the receiver stays frozen until the next keyframe.
            // Force one every ~2 seconds so recovery is always fast.
            framesSinceSyncRequest++
            if (framesSinceSyncRequest >= profile.fps * SYNC_REQUEST_INTERVAL_SECONDS) {
                framesSinceSyncRequest = 0
                val params = Bundle()
                params.putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0)
                try {
                    c.setParameters(params)
                } catch (_: IllegalStateException) {
                    // Codec busy/released mid-flight: skip; next interval retries.
                }
            }
        } catch (t: Throwable) {
            running = false
            onError(t)
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
            if (index < 0) continue // INFO_TRY_AGAIN_LATER / format or buffers changed

            try {
                val buffer = c.getOutputBuffer(index)
                if (buffer != null && info.size > 0) {
                    val data = ByteArray(info.size)
                    buffer.position(info.offset)
                    buffer.get(data, 0, info.size)

                    if (info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0) {
                        packetizer.cacheParameterSets(data)
                    } else {
                        val rtpTimestamp = info.presentationTimeUs * 90 / 1000
                        sender.send(packetizer.packetize(data, rtpTimestamp))
                    }
                }
                c.releaseOutputBuffer(index, false)
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

    private inline fun MediaFormat.trySet(block: MediaFormat.() -> Unit) {
        try {
            block()
        } catch (_: Throwable) {
            // best-effort format hint
        }
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
        private const val DEFAULT_BIT_RATE = 4_000_000
        private const val I_FRAME_INTERVAL_SECONDS = 1
        private const val SYNC_REQUEST_INTERVAL_SECONDS = 2
        private const val CBR_MODE = MediaCodecInfo.EncoderCapabilities.BITRATE_MODE_CBR
        private const val DEQUEUE_TIMEOUT_US = 10_000L
        private const val STOP_JOIN_MS = 500L
    }
}
