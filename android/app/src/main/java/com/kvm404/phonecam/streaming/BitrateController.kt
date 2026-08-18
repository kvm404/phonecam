package com.kvm404.phonecam.streaming

/**
 * Preset bitrate table and loss-driven adaptation. Pure Kotlin so it is JVM-tested;
 * [VideoEncoder] applies the decisions through MediaCodec setParameters.
 *
 * Cap is the table value for the committed canvas and is never raised. Floor is
 * [FLOOR_BPS]. After two consecutive 1 s windows with at least [DROP_THRESHOLD]
 * input drops, bitrate is multiplied by 0.7 ([noteReceiverAge] of 500 ms or more
 * triggers the same step). Two further bad windows at the floor skip every other
 * encode. Ten healthy seconds (no drops, last RTP age unknown or under 200 ms)
 * raise bitrate by 1.15 up to the cap.
 */
class BitrateController(
    val capBps: Int,
    val floorBps: Int = FLOOR_BPS,
    private val nowMs: () -> Long = { System.currentTimeMillis() },
) {
    init {
        require(floorBps > 0) { "floorBps must be > 0" }
        require(capBps >= floorBps) { "capBps must be >= floorBps" }
    }

    private var bitrateBps: Int = capBps
    private var publishedBitrateBps: Int = capBps
    private var skipAlternateFrames = false
    private var nextSkip = false
    private var pendingForceSync = false
    private var requestKeyframe = false
    private var lastRtpMs: Long? = null

    private var windowStartMs: Long = nowMs()
    private var dropsInWindow: Int = 0
    private var consecutiveBadWindows: Int = 0
    private var lastUnhealthyMs: Long? = null
    private var lastStepDownMs: Long? = null
    private var lastStepUpMs: Long? = null
    private var rtpHealthySinceMs: Long? = null

    fun bitrate(): Int = bitrateBps

    fun skipEveryOther(): Boolean = skipAlternateFrames

    fun syncIntervalSeconds(): Int =
        if (isDegraded()) DEGRADED_SYNC_SECONDS else HEALTHY_SYNC_SECONDS

    /**
     * True once after a step-down or [noteRequestKeyframe]. [VideoEncoder] consumes
     * this to fire PARAMETER_KEY_REQUEST_SYNC_FRAME after a successful queue.
     */
    fun consumeForceSync(): Boolean {
        val force = pendingForceSync
        pendingForceSync = false
        return force
    }

    /** Re-arm [consumeForceSync] if MediaCodec rejected the last setParameters. */
    fun restoreForceSync() {
        pendingForceSync = true
    }

    /** True once after [bitrate] changes so the encoder can apply PARAMETER_KEY_VIDEO_BITRATE. */
    fun consumeApplyBitrate(): Boolean {
        if (bitrateBps == publishedBitrateBps) return false
        publishedBitrateBps = bitrateBps
        return true
    }

    /** Re-arm [consumeApplyBitrate] if MediaCodec rejected the last setParameters. */
    fun restoreApplyBitrate() {
        publishedBitrateBps = publishedBitrateBps - 1
    }

    /** Close completed 1 s windows and maybe step up. */
    fun tick() {
        val now = nowMs()
        rollWindows(now)
        maybeStepUp(now)
    }

    /** Count one `dequeueInputBuffer(0) < 0` drop in the current 1 s window. */
    fun onInputDrop() {
        val now = nowMs()
        rollWindows(now)
        dropsInWindow++
        markUnhealthy(now)
        maybeStepUp(now)
    }

    /**
     * When skip-every-other is armed, returns true on every other call so the
     * encoder can drop the frame before dequeue. First opportunity after arming
     * is encoded.
     */
    fun shouldSkipEncode(): Boolean {
        tick()
        if (!skipAlternateFrames) {
            nextSkip = false
            return false
        }
        val skip = nextSkip
        nextSkip = !nextSkip
        return skip
    }

    /**
     * Receiver RTP age from GET /status `last_rtp_ms`. Call this on **every**
     * status read. Age above 400 ms switches to the 1 s sync cadence; ≤ 400
     * clears it. Age ≥ 500 steps bitrate down (at most once per window).
     */
    fun noteReceiverAge(lastRtpMs: Long) {
        val age = if (lastRtpMs < 0) 0 else lastRtpMs
        this.lastRtpMs = age
        val now = nowMs()
        requestKeyframe = age > REQUEST_KEYFRAME_MS
        if (age >= RTP_UNHEALTHY_MS) {
            rtpHealthySinceMs = null
            markUnhealthy(now)
        } else if (rtpHealthySinceMs == null) {
            rtpHealthySinceMs = now
        }
        rollWindows(now)
        if (age >= RTP_STEP_DOWN_MS) {
            val lastDown = lastStepDownMs
            if (lastDown == null || now - lastDown >= WINDOW_MS) {
                stepDown(now)
            }
        }
        maybeStepUp(now)
    }

    /**
     * Immediate sync one-shot for `/status.request_keyframe`. Does not latch the
     * 1 s cadence — that comes from [noteReceiverAge] when last_rtp_ms > 400.
     * Do not call this without also passing the matching last_rtp_ms.
     */
    fun noteRequestKeyframe() {
        pendingForceSync = true
    }

    private fun isDegraded(): Boolean {
        val rtp = lastRtpMs
        return bitrateBps < capBps ||
            skipAlternateFrames ||
            requestKeyframe ||
            (rtp != null && rtp >= RTP_STEP_DOWN_MS)
    }

    private fun rollWindows(now: Long) {
        if (now < windowStartMs) {
            windowStartMs = now
            return
        }
        while (now - windowStartMs >= WINDOW_MS) {
            closeWindow(windowStartMs + WINDOW_MS)
            windowStartMs += WINDOW_MS
        }
    }

    private fun closeWindow(closedAtMs: Long) {
        val bad = dropsInWindow >= DROP_THRESHOLD
        dropsInWindow = 0
        if (!bad) {
            consecutiveBadWindows = 0
            return
        }
        consecutiveBadWindows++
        if (consecutiveBadWindows < BAD_WINDOWS_TO_STEP) return
        consecutiveBadWindows = 0
        val alreadyAtFloor = bitrateBps <= floorBps
        stepDown(closedAtMs)
        if (alreadyAtFloor) {
            skipAlternateFrames = true
        }
    }

    private fun stepDown(now: Long) {
        val next = maxBps(floorBps, scale(bitrateBps, STEP_DOWN_NUM, STEP_DOWN_DEN))
        bitrateBps = next
        lastStepDownMs = now
        markUnhealthy(now)
        pendingForceSync = true
    }

    private fun maybeStepUp(now: Long) {
        val rtp = lastRtpMs
        if (rtp != null) {
            val healthySince = rtpHealthySinceMs
            if (healthySince == null || now - healthySince < HEALTHY_MS) return
        }
        if (bitrateBps >= capBps && !skipAlternateFrames) return
        val lastUnhealthy = lastUnhealthyMs
        if (lastUnhealthy != null && now - lastUnhealthy < HEALTHY_MS) return
        val lastUp = lastStepUpMs
        if (lastUp != null && now - lastUp < HEALTHY_MS) return
        val next = minBps(capBps, scale(bitrateBps, STEP_UP_NUM, STEP_UP_DEN))
        bitrateBps = next
        skipAlternateFrames = false
        nextSkip = false
        lastStepUpMs = now
    }

    private fun markUnhealthy(now: Long) {
        lastUnhealthyMs = now
    }

    companion object {
        const val FLOOR_BPS = 400_000
        const val DROP_THRESHOLD = 5
        const val BAD_WINDOWS_TO_STEP = 2
        const val HEALTHY_SYNC_SECONDS = 2
        const val DEGRADED_SYNC_SECONDS = 1

        private const val WINDOW_MS = 1_000L
        private const val HEALTHY_MS = 10_000L
        private const val RTP_STEP_DOWN_MS = 500L
        private const val RTP_UNHEALTHY_MS = 200L
        private const val REQUEST_KEYFRAME_MS = 400L
        private const val STEP_DOWN_NUM = 7
        private const val STEP_DOWN_DEN = 10
        private const val STEP_UP_NUM = 115
        private const val STEP_UP_DEN = 100

        private const val HIGH_PIXELS = 1280 * 720
        private const val MEDIUM_PIXELS = 960 * 540
        private const val HIGH_30 = 4_000_000
        private const val HIGH_15 = 2_500_000
        private const val MEDIUM_30 = 2_500_000
        private const val MEDIUM_15 = 1_500_000
        private const val LOW_30 = 1_200_000
        private const val LOW_15 = 700_000

        fun targetFor(width: Int, height: Int, fps: Int): Int {
            val pixels = width * height
            val lowFps = fps in 1..15
            return when {
                pixels >= HIGH_PIXELS -> if (lowFps) HIGH_15 else HIGH_30
                pixels >= MEDIUM_PIXELS -> if (lowFps) MEDIUM_15 else MEDIUM_30
                else -> if (lowFps) LOW_15 else LOW_30
            }
        }

        private fun scale(value: Int, num: Int, den: Int): Int =
            ((value.toLong() * num) / den).toInt()

        private fun maxBps(a: Int, b: Int): Int = if (a > b) a else b

        private fun minBps(a: Int, b: Int): Int = if (a < b) a else b
    }
}
