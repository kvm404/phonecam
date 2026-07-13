package com.kvm404.phonecam.streaming

import android.media.Image
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
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

    @Volatile
    private var running = false

    /** Configure and start the encoder plus its output-draining thread. */
    fun start() {
        try {
            val format = MediaFormat.createVideoFormat(MIME, profile.width, profile.height).apply {
                setInteger(
                    MediaFormat.KEY_COLOR_FORMAT,
                    MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420Flexible,
                )
                setInteger(MediaFormat.KEY_BIT_RATE, bitRate)
                setInteger(MediaFormat.KEY_FRAME_RATE, profile.fps)
                setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, I_FRAME_INTERVAL_SECONDS)
                // VBR and baseline profile are best-effort: not every device honours them.
                trySet { setInteger(MediaFormat.KEY_BITRATE_MODE, VBR_MODE) }
                trySet {
                    setInteger(
                        MediaFormat.KEY_PROFILE,
                        MediaCodecInfo.CodecProfileLevel.AVCProfileBaseline,
                    )
                }
            }

            val c = MediaCodec.createEncoderByType(MIME)
            c.configure(format, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            c.start()
            codec = c
            running = true

            outputThread = Thread({ drainOutput(c) }, "phonecam-encoder-output").also { it.start() }
        } catch (t: Throwable) {
            running = false
            releaseCodecQuietly()
            onError(t)
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

            val image = c.getInputImage(index)
            if (image == null) {
                c.queueInputBuffer(index, 0, 0, frame.timestampUs, 0)
                return
            }
            fillImage(image, frame)
            val size = frame.width * frame.height * 3 / 2
            c.queueInputBuffer(index, 0, size, frame.timestampUs, 0)
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
        private const val MIME = "video/avc"
        private const val DEFAULT_BIT_RATE = 4_000_000
        private const val I_FRAME_INTERVAL_SECONDS = 1
        private const val VBR_MODE = MediaCodecInfo.EncoderCapabilities.BITRATE_MODE_VBR
        private const val DEQUEUE_TIMEOUT_US = 10_000L
        private const val STOP_JOIN_MS = 500L
    }
}
