package com.kvm404.phonecam

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.Binder
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.util.Log
import android.util.Size
import android.view.OrientationEventListener
import android.widget.Toast
import androidx.camera.core.Camera
import androidx.camera.core.CameraSelector
import androidx.camera.core.ExperimentalGetImage
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.core.ZoomState
import androidx.camera.core.resolutionselector.ResolutionSelector
import androidx.camera.core.resolutionselector.ResolutionStrategy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.Observer
import com.kvm404.phonecam.pairing.CameraFacing
import com.kvm404.phonecam.pairing.HttpControlClient
import com.kvm404.phonecam.pairing.PairingPayload
import com.kvm404.phonecam.pairing.PhoneIdentity
import com.kvm404.phonecam.pairing.ReconnectController
import com.kvm404.phonecam.pairing.ReconnectResult
import com.kvm404.phonecam.pairing.RtpEndpoint
import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.SessionCredentials
import com.kvm404.phonecam.pairing.StatusResult
import com.kvm404.phonecam.pairing.StreamHealth
import com.kvm404.phonecam.pairing.VideoProfile
import com.kvm404.phonecam.pairing.deviceOrientationToSurfaceRotation
import com.kvm404.phonecam.pairing.quantizeOrientation
import com.kvm404.phonecam.streaming.BitrateController
import com.kvm404.phonecam.streaming.CanvasComposer
import com.kvm404.phonecam.streaming.FrameConverter
import com.kvm404.phonecam.streaming.RtpPacketizer
import com.kvm404.phonecam.streaming.UdpRtpSender
import com.kvm404.phonecam.streaming.VideoEncoder
import com.kvm404.phonecam.streaming.ZoomStepper
import java.net.InetSocketAddress
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * In-process handoff of the session from the pairing activity to the streaming service.
 *
 * The [RtpIdentity] owns an open UDP socket that cannot be parcelled through an Intent, and
 * the app is single-process, so the activity drops the paired session here and the service
 * picks it up in [StreamingService.onStartCommand]. Ownership of the socket transfers to the
 * service, which closes it when streaming ends.
 */
object StreamingSession {
    @Volatile var payload: PairingPayload? = null
    @Volatile var rtpIdentity: RtpIdentity? = null
    @Volatile var profile: VideoProfile? = null
    @Volatile var resumeToken: String? = null
    @Volatile var pairingSecret: String? = null
    @Volatile var phone: PhoneIdentity? = null

    data class Handoff(
        val payload: PairingPayload,
        val profile: VideoProfile,
        val rtpIdentity: RtpIdentity,
        val resumeToken: String?,
        val pairingSecret: String?,
        val phone: PhoneIdentity?,
    )

    @Synchronized
    fun take(): Handoff? {
        val currentPayload = payload ?: return null
        val currentProfile = profile ?: return null
        val currentRtp = rtpIdentity ?: return null
        val currentResume = resumeToken
        val currentSecret = pairingSecret
        val currentPhone = phone
        payload = null
        profile = null
        rtpIdentity = null
        resumeToken = null
        pairingSecret = null
        phone = null
        return Handoff(
            currentPayload, currentProfile, currentRtp,
            currentResume, currentSecret, currentPhone,
        )
    }

    @Synchronized
    fun clearAndClose() {
        rtpIdentity?.close()
        payload = null
        rtpIdentity = null
        profile = null
        resumeToken = null
        pairingSecret = null
        phone = null
    }

    @Synchronized
    fun clear() {
        payload = null
        rtpIdentity = null
        profile = null
        resumeToken = null
        pairingSecret = null
        phone = null
    }
}

/**
 * Immutable snapshot of the streaming camera's zoom, read off the live
 * [androidx.camera.core.ZoomState]. Null while no camera is bound (or no zoom state has been
 * delivered yet) so the activity can hide the zoom controls until the range is known.
 */
data class ZoomInfo(
    val ratio: Float,
    val minRatio: Float,
    val maxRatio: Float,
)

/**
 * Camera foreground service that OWNS the streaming pipeline so it survives the screen
 * turning off, the lock screen, and the activity being destroyed — the whole point of the
 * feature. It mirrors what [MainActivity] used to do inline: CameraX (Preview +
 * ImageAnalysis) bound to the SERVICE lifecycle, the fixed-canvas analyzer chain
 * (crop -> rotate -> compose -> encode -> send), the [VideoEncoder], a low-latency Wi-Fi
 * lock, a PARTIAL_WAKE_LOCK (so encoding keeps running with the screen off) and the
 * sensor-driven [OrientationEventListener].
 *
 * The pure classes ([FrameConverter], [CanvasComposer], [RtpPacketizer], [VideoEncoder],
 * the orientation helpers) are reused verbatim.
 *
 * The activity binds via [LocalBinder] to attach/detach its live preview, flip the camera,
 * drive the streaming camera's zoom, stop the stream, and observe start/stop through a
 * [Callback]. Streaming is unaffected by
 * the activity binding, unbinding, or being destroyed; it stops only on the Leave button,
 * the notification Stop action, an encoder/camera error, or a 60 s reconnect give-up.
 * Send errors and brief Wi-Fi loss do not stop it.
 */
@ExperimentalGetImage
class StreamingService : LifecycleService() {

    /** Observes streaming lifecycle so a bound activity can reflect it in its UI. */
    interface Callback {
        /** Streaming is live (pipeline started successfully). */
        fun onStreamingStarted()

        /** Streaming ended. [error] is non-null on an encoder/camera failure. */
        fun onStreamingStopped(error: String?)

        /** In-session reconnect health. Default no-op for callers that only watch start/stop. */
        fun onStreamHealth(health: StreamHealth) {}

        /** Provider/bind settled so the activity can show or hide Flip. */
        fun onCameraReady() {}

        /** Flip or first-bind facing was unavailable; the previous lens is still live. */
        fun onNoOtherCamera() {}

        /** The streaming camera's zoom state/range changed; refresh the zoom row. */
        fun onZoomChanged() {}

        /** Stream mirror state changed; refresh the mirror toggle and viewfinder inversion. */
        fun onMirrorChanged(isMirrored: Boolean) {}
    }

    inner class LocalBinder : Binder() {
        val service: StreamingService get() = this@StreamingService
    }

    private val binder = LocalBinder()

    private val analysisExecutor = Executors.newSingleThreadExecutor()
    private val serviceJob = SupervisorJob()
    private val serviceScope = CoroutineScope(serviceJob + Dispatchers.Main.immediate)
    private val controlClient = HttpControlClient()
    private val wifiUp = AtomicBoolean(true)
    /** Matching Wi-Fi networks (not a last-writer flag). Empty means Wi-Fi is down. */
    private val wifiNetworks = ConcurrentHashMap.newKeySet<Network>()

    private var cameraProvider: ProcessCameraProvider? = null
    private var preview: Preview? = null
    private var imageAnalysis: ImageAnalysis? = null

    /**
     * The [Camera] handle returned by the streaming bindToLifecycle, retained so the zoom
     * controls can drive [Camera.getCameraControl]. Null while no camera is bound.
     */
    private var camera: Camera? = null
    private var zoomObserver: Observer<ZoomState>? = null

    /** Latest zoom snapshot from the live camera; written on the main thread. */
    @Volatile
    private var zoomInfo: ZoomInfo? = null

    /** Set when a camera binds (stream start, flip) so its first zoom state resets to 1x. */
    @Volatile
    private var pendingZoomReset = false

    private var canvas: VideoProfile? = null
    private var payload: PairingPayload? = null
    private var rtpIdentity: RtpIdentity? = null
    private var resumeToken: String? = null
    private var pairingSecret: String? = null
    private var phone: PhoneIdentity? = null
    private var videoEncoder: VideoEncoder? = null
    private var rtpSender: UdpRtpSender? = null
    private var reconnectController: ReconnectController? = null
    private var watchdogJob: Job? = null
    private var healthJob: Job? = null
    @Volatile
    private var lastSeenSendFailures = 0
    private var wifiCallbackRegistered = false
    private var wifiCallbackPrimed = false

    private val wifiCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            val first = !wifiCallbackPrimed
            wifiCallbackPrimed = true
            wifiNetworks.add(network)
            adoptLiveWifiNetworks()
            Log.i(TAG, "wifi available")
            if (!first) startReconnect("wifi available")
        }

        override fun onLost(network: Network) {
            wifiCallbackPrimed = true
            wifiNetworks.remove(network)
            adoptLiveWifiNetworks(excluding = network)
            Log.i(TAG, "wifi lost")
            startReconnect("wifi lost")
        }
    }

    private var wifiLock: WifiManager.WifiLock? = null
    private var wakeLock: PowerManager.WakeLock? = null

    private var cameraSelector: CameraSelector = CameraSelector.DEFAULT_BACK_CAMERA

    @Volatile
    private var deviceOrientation = 0
    private var orientationListener: OrientationEventListener? = null

    private var callback: Callback? = null

    /**
     * A terminal stop that happened before any callback was attached (e.g. the encoder failed to
     * start before the activity finished binding). Latched here and replayed in [setCallback] so
     * the UI is never left stuck on "Connecting".
     */
    private var pendingStop = false
    private var pendingStopError: String? = null

    @Volatile
    private var streaming = false

    @Volatile
    private var isMirrored = false

    /** First-bind fell back; publish cameraLabel() once reconnectController exists. */
    @Volatile
    private var reportCameraAfterRecovery = false

    // ------------------------------------------------------------------ Service lifecycle

    override fun onCreate() {
        super.onCreate()
        isRunning = true
        createNotificationChannel()
        applyPersistedFacing()
        loadMirrorPreference()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        if (intent?.action == ACTION_STOP) {
            startForegroundNotification()
            stopStreaming(error = null)
            return START_NOT_STICKY
        }
        // A default start: pick up the paired session and go live.
        if (!streaming) startStreaming()
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent): IBinder {
        super.onBind(intent)
        return binder
    }

    override fun onDestroy() {
        // Ensure everything is released even on an abrupt teardown.
        releasePipeline()
        serviceJob.cancel()
        analysisExecutor.shutdown()
        isRunning = false
        super.onDestroy()
    }

    // ------------------------------------------------------------------ Binder API (activity)

    fun setCallback(cb: Callback?) {
        callback = cb
        // Replay a stop/error that fired before this callback attached (start-failure race),
        // so a late-binding activity still leaves "Connecting" and shows the error.
        if (cb != null && pendingStop) {
            pendingStop = false
            val error = pendingStopError
            pendingStopError = null
            cb.onStreamingStopped(error)
        }
    }

    fun isStreaming(): Boolean = streaming

    fun streamHealth(): StreamHealth =
        reconnectController?.health?.value ?: StreamHealth.Live

    fun laptopName(): String? = payload?.name

    fun profile(): VideoProfile? = canvas

    /** Attach the activity's live preview while it is visible; safe to call repeatedly. */
    fun attachPreview(surfaceProvider: Preview.SurfaceProvider) {
        preview?.surfaceProvider = surfaceProvider
    }

    /** Detach the preview when the activity is not visible; streaming is unaffected. */
    fun detachPreview() {
        preview?.surfaceProvider = null
    }

    fun canFlipCamera(): Boolean {
        val provider = cameraProvider ?: return false
        return try {
            // Flip binds DEFAULT_BACK/FRONT; extra BACK or EXTERNAL infos do not count.
            provider.hasCamera(CameraSelector.DEFAULT_BACK_CAMERA) &&
                provider.hasCamera(CameraSelector.DEFAULT_FRONT_CAMERA)
        } catch (_: Exception) {
            false
        }
    }

    fun flipCamera() {
        if (!streaming) return
        val previous = cameraSelector
        cameraSelector = oppositeSelector(previous)
        bindCamera(isFlip = true, previous = previous)
    }

    fun isMirrored(): Boolean = isMirrored

    fun isFrontCamera(): Boolean = cameraSelector == CameraSelector.DEFAULT_FRONT_CAMERA

    fun toggleMirror(): Boolean {
        val next = !isMirrored
        isMirrored = next
        persistMirror()
        callback?.onMirrorChanged(next)
        return next
    }

    /** Latest zoom snapshot from the streaming camera, or null when no range is known. */
    fun currentZoom(): ZoomInfo? = zoomInfo

    /** One 0.25x step in, clamped to the live camera's max ratio. */
    fun zoomIn() {
        applyZoom { info -> ZoomStepper.stepUp(info.ratio, info.maxRatio) }
    }

    /** One 0.25x step out, clamped to the live camera's min ratio. */
    fun zoomOut() {
        applyZoom { info -> ZoomStepper.stepDown(info.ratio, info.minRatio) }
    }

    /** Reset to 1x, clamped into the live camera's range. */
    fun resetZoom() {
        applyZoom { info -> ZoomStepper.resetTarget(info.minRatio, info.maxRatio) }
    }

    private fun applyZoom(step: (ZoomInfo) -> Float) {
        if (!streaming) return
        val info = zoomInfo ?: return
        camera?.cameraControl?.setZoomRatio(step(info))
    }

    /** User-initiated stop (Leave button). Tears down and removes the service. */
    fun stopFromActivity() {
        stopStreaming(error = null)
    }

    // ------------------------------------------------------------------ Streaming pipeline

    private fun startStreaming() {
        val handoff = StreamingSession.take()
        val socket = handoff?.rtpIdentity?.socket
        if (handoff == null || socket == null) {
            handoff?.rtpIdentity?.close()
            // Nothing to stream (e.g. the session was cleared/closed on Cancel).
            startForegroundNotification()
            stopStreaming(error = null)
            return
        }
        val current = handoff.payload
        payload = current
        canvas = handoff.profile
        rtpIdentity = handoff.rtpIdentity
        resumeToken = handoff.resumeToken
        pairingSecret = handoff.pairingSecret
        phone = handoff.phone

        startForegroundNotification(current.name)

        val target = InetSocketAddress(current.rtpHost, current.rtpPort)
        val sender = UdpRtpSender(socket, target)
        rtpSender = sender
        val packetizer = RtpPacketizer(handoff.rtpIdentity.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        val committed = handoff.profile
        val encoder = VideoEncoder(
            committed,
            packetizer,
            sender,
            BitrateController.targetFor(committed.width, committed.height, committed.fps),
        ) { error ->
            ContextCompat.getMainExecutor(this).execute {
                stopStreaming(error = error.localizedMessage ?: error.toString())
            }
        }
        videoEncoder = encoder
        try {
            encoder.start()
        } catch (e: Exception) {
            stopStreaming(error = e.localizedMessage ?: e.toString())
            return
        }

        acquireLocks()
        startOrientationTracking()
        applyPersistedFacing()
        streaming = true
        bindCamera()
        startSessionRecovery(current, committed, handoff)
        callback?.onStreamingStarted()
    }

    private fun stopStreaming(error: String?) {
        val wasStreaming = streaming
        releasePipeline()
        stopForeground(STOP_FOREGROUND_REMOVE)
        if (wasStreaming || error != null) {
            val cb = callback
            if (cb != null) {
                cb.onStreamingStopped(error)
            } else {
                // No activity bound yet (e.g. encoder failed to start before binding finished):
                // latch this terminal stop so [setCallback] replays it once the activity attaches.
                pendingStop = true
                pendingStopError = error
            }
        }
        stopSelf()
    }

    /** Release the camera, encoder, socket, locks and listener. Idempotent. */
    private fun releasePipeline() {
        streaming = false
        isMirrored = false
        reconnectController?.cancel()
        watchdogJob?.cancel()
        healthJob?.cancel()
        watchdogJob = null
        healthJob = null
        reconnectController = null
        reportCameraAfterRecovery = false
        unregisterWifiCallback()
        stopOrientationTracking()
        orientationListener = null
        zoomObserver?.let { camera?.cameraInfo?.zoomState?.removeObserver(it) }
        zoomObserver = null
        camera = null
        zoomInfo = null
        pendingZoomReset = false
        imageAnalysis?.clearAnalyzer()
        imageAnalysis = null
        preview = null
        cameraProvider?.unbindAll()
        videoEncoder?.stop()
        videoEncoder = null
        rtpSender = null
        releaseLocks()
        rtpIdentity?.close()
        rtpIdentity = null
    }

    private fun bindCamera(isFlip: Boolean = false, previous: CameraSelector? = null) {
        loadMirrorPreference()
        val provider = cameraProvider
        if (provider != null) {
            bindCamera(provider, isFlip, previous)
            return
        }
        val future = ProcessCameraProvider.getInstance(this)
        future.addListener({
            try {
                val resolved = future.get()
                cameraProvider = resolved
                if (streaming) bindCamera(resolved, isFlip, previous)
            } catch (e: Exception) {
                stopStreaming(error = e.localizedMessage ?: e.toString())
            }
        }, ContextCompat.getMainExecutor(this))
    }

    private fun bindCamera(
        provider: ProcessCameraProvider,
        isFlip: Boolean,
        previous: CameraSelector?,
    ) {
        val requested = cameraSelector
        // Probe before unbindAll so a missing opposite lens cannot tear down a live bind.
        if (!providerHasCamera(provider, requested)) {
            if (isFlip) {
                cameraSelector = previous ?: oppositeSelector(requested)
                loadMirrorPreference()
                notifyNoOtherCamera()
                callback?.onCameraReady()
                return
            }
            val fallback = oppositeSelector(requested)
            if (!providerHasCamera(provider, fallback)) {
                stopStreaming(error = "no camera available")
                return
            }
            cameraSelector = fallback
            loadMirrorPreference()
            try {
                bindUseCasesAfterUnbind(provider, fallback)
            } catch (e: Exception) {
                stopStreaming(error = e.localizedMessage ?: e.toString())
                return
            }
            persistFacing()
            notifyNoOtherCamera()
            callback?.onCameraReady()
            scheduleBoundCameraReport()
            return
        }
        loadMirrorPreference()
        try {
            bindUseCasesAfterUnbind(provider, requested)
            if (isFlip) persistFacing()
            callback?.onCameraReady()
        } catch (e: Exception) {
            if (isFlip) {
                cameraSelector = previous ?: oppositeSelector(requested)
                loadMirrorPreference()
                try {
                    bindUseCasesAfterUnbind(provider, cameraSelector, resetTo1x = false)
                } catch (rebind: Exception) {
                    stopStreaming(error = rebind.localizedMessage ?: rebind.toString())
                    return
                }
                notifyNoOtherCamera()
                callback?.onCameraReady()
                return
            }
            stopStreaming(error = e.localizedMessage ?: e.toString())
        }
    }

    private fun providerHasCamera(
        provider: ProcessCameraProvider,
        selector: CameraSelector,
    ): Boolean = try {
        provider.hasCamera(selector)
    } catch (_: Exception) {
        false
    }

    private fun bindUseCasesAfterUnbind(
        provider: ProcessCameraProvider,
        selector: CameraSelector,
        resetTo1x: Boolean = true,
    ) {
        val analysis = buildAnalysis()
        // Seed the freshly-bound analyzer with the current device orientation so the first
        // frames (and any post-flip frames) are already upright before the sensor next fires.
        analysis.targetRotation = deviceOrientationToSurfaceRotation(deviceOrientation)
        imageAnalysis = analysis
        provider.unbindAll()
        preview = null
        bindUseCases(provider, selector, analysis, resetTo1x)
    }

    private fun bindUseCases(
        provider: ProcessCameraProvider,
        selector: CameraSelector,
        analysis: ImageAnalysis,
        resetTo1x: Boolean,
    ) {
        // The local viewfinder is always on: Preview is bound together with ImageAnalysis
        // for the whole session.
        val previewUseCase = Preview.Builder().build()
        preview = previewUseCase
        adoptCamera(provider.bindToLifecycle(this, selector, previewUseCase, analysis), resetTo1x)
    }

    /**
     * Track a freshly-bound streaming camera: retain the handle for the zoom controls, watch
     * its zoom state so the LIVE readout and button bounds follow the real lens, and — for a
     * new bind (stream start, camera flip) — reset to 1x once the camera reports its range.
     * A failed-flip fallback rebind on the same lens passes [resetTo1x] = false so the
     * re-opened lens keeps the user's last ratio once it reports.
     */
    private fun adoptCamera(bound: Camera, resetTo1x: Boolean = true) {
        zoomObserver?.let { camera?.cameraInfo?.zoomState?.removeObserver(it) }
        camera = bound
        if (resetTo1x) {
            // Drop the previous lens' snapshot: the range is unknown until the new camera
            // opens, and the pending reset below applies the correct clamp when it does.
            zoomInfo = null
            pendingZoomReset = true
        }
        val observer = Observer<ZoomState> { state ->
            zoomInfo = ZoomInfo(state.zoomRatio, state.minZoomRatio, state.maxZoomRatio)
            if (pendingZoomReset) {
                pendingZoomReset = false
                camera?.cameraControl?.setZoomRatio(
                    ZoomStepper.resetTarget(state.minZoomRatio, state.maxZoomRatio),
                )
            }
            callback?.onZoomChanged()
        }
        zoomObserver = observer
        bound.cameraInfo.zoomState.observeForever(observer)
    }

    private fun buildAnalysis(): ImageAnalysis {
        // Target the SESSION canvas dims (the chosen [StreamQuality], threaded in as the
        // committed profile). This is the key CPU win on weak devices: a smaller analysis
        // resolution means the per-frame crop -> rotate -> compose -> pack chain runs on
        // fewer pixels. Falls back to 720p only if the canvas is somehow unset.
        val target = canvas?.let { Size(it.width, it.height) } ?: Size(1280, 720)
        val resolutionSelector = ResolutionSelector.Builder()
            .setResolutionStrategy(
                ResolutionStrategy(
                    target,
                    ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
                )
            )
            .build()
        return ImageAnalysis.Builder()
            .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
            .setResolutionSelector(resolutionSelector)
            .build()
            .also { it.setAnalyzer(analysisExecutor, ::analyzeForStreaming) }
    }

    /**
     * Fixed-canvas analyzer, identical to the one that used to live in the activity:
     * center-crop the landscape sensor buffer to the canvas dims, rotate by the
     * sensor-correct `rotationDegrees`, and compose back onto the fixed canvas so the encoder
     * always receives exactly the canvas dims. Rotating mid-stream needs no renegotiation.
     */
    private fun analyzeForStreaming(imageProxy: ImageProxy) {
        try {
            val canvas = canvas ?: return
            val rotation = imageProxy.imageInfo.rotationDegrees
            val planes = imageProxy.planes
            val cropped = FrameConverter.toFrameData(
                width = imageProxy.width,
                height = imageProxy.height,
                yBuffer = planes[0].buffer,
                yRowStride = planes[0].rowStride,
                yPixelStride = planes[0].pixelStride,
                uBuffer = planes[1].buffer,
                uRowStride = planes[1].rowStride,
                uPixelStride = planes[1].pixelStride,
                vBuffer = planes[2].buffer,
                vRowStride = planes[2].rowStride,
                vPixelStride = planes[2].pixelStride,
                timestampUs = imageProxy.imageInfo.timestamp / 1000,
                targetWidth = canvas.width,
                targetHeight = canvas.height,
            )
            val rotated = FrameConverter.rotate(cropped, rotation)
            if (isMirrored) FrameConverter.flipHorizontallyInPlace(rotated)
            val frame = CanvasComposer.compose(rotated, canvas.width, canvas.height)
            videoEncoder?.encode(frame)
        } catch (e: IllegalArgumentException) {
            ContextCompat.getMainExecutor(this).execute {
                stopStreaming(error = e.message ?: e.toString())
            }
        } catch (_: Exception) {
            // Drop this frame; a transient conversion/encode error must not stop the stream.
        } finally {
            imageProxy.close()
        }
    }

    // ------------------------------------------------------------------ In-session reconnect

    private fun startSessionRecovery(
        current: PairingPayload,
        committed: VideoProfile,
        handoff: StreamingSession.Handoff,
    ) {
        lastSeenSendFailures = 0
        wifiNetworks.clear()
        wifiCallbackPrimed = false
        registerWifiCallback()
        val identity = phone ?: handoff.phone
        if (identity != null) {
            val controller = ReconnectController(
                client = controlClient,
                creds = SessionCredentials(
                    payload = current,
                    resumeToken = handoff.resumeToken ?: resumeToken,
                    pairingSecret = handoff.pairingSecret ?: pairingSecret,
                    profile = committed,
                    phone = identity,
                ),
                wifiAvailable = { wifiUp.get() },
                resolveIdentity = { resolveRtpIdentity() },
                camera = { cameraLabel() },
                onSuccess = { result, _ -> applyReconnectSuccess(result) },
            )
            reconnectController = controller
            healthJob = serviceScope.launch {
                controller.health.collect { health ->
                    callback?.onStreamHealth(health)
                    if (health is StreamHealth.Failed && streaming) {
                        Log.i(TAG, "reconnect gave up: ${health.message}")
                        stopStreaming(health.message)
                    }
                }
            }
        }
        watchdogJob = serviceScope.launch(Dispatchers.IO) {
            while (isActive && streaming) {
                delay(WATCHDOG_INTERVAL_MS)
                if (streaming) pollLaptopStatus()
            }
        }
        publishBoundCamera()
    }

    private fun startReconnect(reason: String) {
        val controller = reconnectController ?: return
        if (!streaming) return
        if (controller.health.value is StreamHealth.Failed) return
        Log.i(TAG, "reconnect start: $reason")
        serviceScope.launch(Dispatchers.IO) {
            controller.start()
        }
    }

    /** New port/SSRC every attempt so RTP is not stuck on a socket bound to a dead iface. */
    private fun resolveRtpIdentity(): RtpEndpoint {
        val created = RtpIdentity.create()
        val existing = rtpIdentity
        created.socket?.let { rtpSender?.setSocket(it) }
        videoEncoder?.replaceRtpIdentity(created)
        rtpIdentity = created
        if (existing != null && existing !== created) {
            existing.close()
        }
        Log.i(TAG, "recreated rtp identity port=${created.sourcePort} ssrc=${created.ssrc}")
        return RtpEndpoint(created.sourcePort, created.ssrc)
    }

    private fun applyReconnectSuccess(result: ReconnectResult.Ok) {
        lastSeenSendFailures = videoEncoder?.sendFailures() ?: lastSeenSendFailures
        val current = payload ?: return
        var next = current
        if (result.session.isNotBlank()) {
            next = next.copy(session = result.session)
        }
        if (result.control.isNotBlank()) {
            next = next.copy(control = result.control)
        }
        val rtpField = result.rtp
        if (rtpField.isNotBlank()) {
            val colon = rtpField.lastIndexOf(':')
            val host = if (colon > 0) rtpField.substring(0, colon) else ""
            val port = if (colon in 1 until rtpField.length - 1) {
                rtpField.substring(colon + 1).toIntOrNull()
            } else {
                null
            }
            if (host.isNotBlank() && port != null && port in 1..65535) {
                rtpSender?.setTarget(InetSocketAddress(host, port))
                next = next.copy(rtp = rtpField, rtpHost = host, rtpPort = port)
            }
        }
        payload = next
        if (result.resumeToken.isNotBlank()) {
            resumeToken = result.resumeToken
        }
        videoEncoder?.requestSyncFrame()
        Log.i(TAG, "reconnect succeeded session=${next.session}")
    }

    private fun pollLaptopStatus() {
        val current = payload ?: return
        val encoder = videoEncoder
        when (val result = controlClient.status(current)) {
            is StatusResult.Failure -> {
                Log.i(TAG, "watchdog: laptop unreachable: ${result.message}")
                startReconnect("watchdog unreachable")
            }
            is StatusResult.Ok -> {
                result.lastRtpMs?.let { encoder?.noteReceiverAge(it) }
                if (result.requestKeyframe) {
                    encoder?.requestSyncFrame()
                    encoder?.noteRequestKeyframe()
                }
                if (ReconnectController.shouldStartOnStatus(
                        reconnectController?.health?.value ?: StreamHealth.Live,
                        result.approved,
                        result.session,
                        payload?.session,
                    )
                ) {
                    Log.i(TAG, "watchdog: unapproved or session mismatch")
                    startReconnect("status mismatch")
                }
            }
        }
        val failures = encoder?.sendFailures() ?: 0
        if (ReconnectController.shouldStartOnSendFailures(
                reconnectController?.health?.value ?: StreamHealth.Live,
                failures,
                lastSeenSendFailures,
            )
        ) {
            lastSeenSendFailures = failures
            startReconnect("send failures")
        }
    }

    private fun cameraLabel(): String =
        if (cameraSelector == CameraSelector.DEFAULT_FRONT_CAMERA) {
            CameraFacing.FRONT
        } else {
            CameraFacing.BACK
        }

    private fun scheduleBoundCameraReport() {
        reportCameraAfterRecovery = true
        publishBoundCamera()
    }

    /**
     * One-shot /reconnect with the live RTP pin so /status camera matches a
     * first-bind fallback. Same port/SSRC and canvas: the laptop does not
     * restart gst. A later in-session reconnect already sends [cameraLabel].
     */
    private fun publishBoundCamera() {
        if (!reportCameraAfterRecovery || !streaming) return
        val controller = reconnectController ?: return
        val identity = rtpIdentity ?: return
        reportCameraAfterRecovery = false
        val endpoint = RtpEndpoint(identity.sourcePort, identity.ssrc)
        Log.i(TAG, "report bound camera=${cameraLabel()}")
        serviceScope.launch(Dispatchers.IO) {
            controller.reportCamera(endpoint)
        }
    }

    private fun applyPersistedFacing() {
        cameraSelector = if (
            CameraFacing.fromPref(prefs().getString(CameraFacing.PREF_KEY, null)) == CameraFacing.FRONT
        ) {
            CameraSelector.DEFAULT_FRONT_CAMERA
        } else {
            CameraSelector.DEFAULT_BACK_CAMERA
        }
    }

    private fun persistFacing() {
        prefs().edit().putString(CameraFacing.PREF_KEY, cameraLabel()).apply()
    }

    private fun persistMirror() {
        prefs().edit().putBoolean("camera_mirror_" + cameraLabel(), isMirrored).apply()
    }

    private fun loadMirrorPreference() {
        val facing = cameraLabel()
        isMirrored = prefs().getBoolean("camera_mirror_" + facing, false)
        callback?.onMirrorChanged(isMirrored)
    }

    private fun prefs() = getSharedPreferences("phonecam", Context.MODE_PRIVATE)

    private fun oppositeSelector(current: CameraSelector): CameraSelector =
        if (current == CameraSelector.DEFAULT_FRONT_CAMERA) {
            CameraSelector.DEFAULT_BACK_CAMERA
        } else {
            CameraSelector.DEFAULT_FRONT_CAMERA
        }

    private fun notifyNoOtherCamera() {
        Log.i(TAG, "no other camera")
        val cb = callback
        if (cb != null) {
            cb.onNoOtherCamera()
        } else {
            Toast.makeText(this, R.string.no_other_camera, Toast.LENGTH_SHORT).show()
        }
    }

    private fun registerWifiCallback() {
        if (wifiCallbackRegistered) return
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        adoptLiveWifiNetworks(cm)
        val request = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        try {
            cm.registerNetworkCallback(request, wifiCallback)
            wifiCallbackRegistered = true
        } catch (e: RuntimeException) {
            Log.i(TAG, "wifi callback not registered: ${e.message}")
            // Cannot observe Wi-Fi; keep the give-up window running.
            wifiUp.set(true)
        }
    }

    private fun unregisterWifiCallback() {
        if (!wifiCallbackRegistered) return
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        try {
            cm.unregisterNetworkCallback(wifiCallback)
        } catch (_: RuntimeException) {
            // already unregistered
        }
        wifiCallbackRegistered = false
        wifiCallbackPrimed = false
        wifiNetworks.clear()
    }

    @Suppress("DEPRECATION")
    private fun adoptLiveWifiNetworks(
        cm: ConnectivityManager? = null,
        excluding: Network? = null,
    ) {
        val manager = cm
            ?: getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
        if (manager != null) {
            try {
                for (network in manager.allNetworks) {
                    if (excluding != null && network == excluding) continue
                    if (isMatchingWifi(manager, network)) {
                        wifiNetworks.add(network)
                    }
                }
            } catch (_: RuntimeException) {
                // leave the callback-tracked set as-is
            }
        }
        wifiUp.set(wifiNetworks.isNotEmpty())
    }

    private fun isMatchingWifi(cm: ConnectivityManager, network: Network): Boolean {
        val caps = cm.getNetworkCapabilities(network) ?: return false
        return caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) &&
            caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
    }

    // ------------------------------------------------------------------ Orientation

    private fun startOrientationTracking() {
        val listener = orientationListener ?: object : OrientationEventListener(this) {
            override fun onOrientationChanged(orientation: Int) {
                val quantized = quantizeOrientation(orientation, deviceOrientation)
                if (quantized != deviceOrientation) {
                    deviceOrientation = quantized
                    imageAnalysis?.targetRotation = deviceOrientationToSurfaceRotation(quantized)
                }
            }
        }.also { orientationListener = it }
        if (listener.canDetectOrientation()) listener.enable()
    }

    private fun stopOrientationTracking() {
        orientationListener?.disable()
    }

    // ------------------------------------------------------------------ Locks

    /**
     * Held while streaming: a low-latency Wi-Fi lock (keeps the radio on-channel so an RTP
     * burst is not dropped every ~10s) and a PARTIAL_WAKE_LOCK (keeps the CPU running so the
     * encoder keeps producing frames with the screen off / the phone locked).
     */
    private fun acquireLocks() {
        if (wifiLock == null) {
            val wifi = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            val mode = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                WifiManager.WIFI_MODE_FULL_LOW_LATENCY
            } else {
                @Suppress("DEPRECATION")
                WifiManager.WIFI_MODE_FULL_HIGH_PERF
            }
            wifiLock = wifi.createWifiLock(mode, "phonecam:stream").apply {
                setReferenceCounted(false)
            }
        }
        wifiLock?.acquire()

        if (wakeLock == null) {
            val power = applicationContext.getSystemService(Context.POWER_SERVICE) as PowerManager
            wakeLock = power.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "phonecam:stream").apply {
                setReferenceCounted(false)
            }
        }
        wakeLock?.acquire(WAKE_LOCK_TIMEOUT_MS)
    }

    private fun releaseLocks() {
        wifiLock?.takeIf { it.isHeld }?.release()
        wakeLock?.takeIf { it.isHeld }?.release()
    }

    // ------------------------------------------------------------------ Notification

    private fun createNotificationChannel() {
        // minSdk is 26, so notification channels always exist.
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.notification_channel_name),
            NotificationManager.IMPORTANCE_LOW,
        )
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(channel)
    }

    private fun startForegroundNotification(laptop: String = "") {
        val notification = buildNotification(laptop)
        // The typed FGS API and the CAMERA type constant are API 30+; on 26–29 the plain
        // startForeground is used (the manifest still declares the camera service type).
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_CAMERA,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun buildNotification(laptop: String): Notification {
        val contentIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java)
                .setFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_REORDER_TO_FRONT),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val stopPending = PendingIntent.getService(
            this,
            1,
            stopIntent(this),
            PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.notification_channel_name))
            .setContentText(getString(R.string.notification_content, laptop))
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setContentIntent(contentIntent)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .addAction(0, getString(R.string.notification_stop), stopPending)
            .build()
    }

    companion object {
        /** True between [onCreate] and [onDestroy]; lets a recreated activity rebind. */
        @Volatile
        var isRunning = false
            private set

        private const val CHANNEL_ID = "phonecam_streaming"
        private const val NOTIFICATION_ID = 1
        private const val ACTION_STOP = "com.kvm404.phonecam.action.STOP"

        /** Same intent as the notification Stop action; works before the activity has a binder. */
        fun stopIntent(context: Context): Intent =
            Intent(context, StreamingService::class.java).setAction(ACTION_STOP)

        /** Safety valve so a wedged stream can never hold the CPU forever. */
        private const val WAKE_LOCK_TIMEOUT_MS = 12L * 60L * 60L * 1000L
        private const val WATCHDOG_INTERVAL_MS = 2_000L
        private const val TAG = "phonecam"
    }
}
