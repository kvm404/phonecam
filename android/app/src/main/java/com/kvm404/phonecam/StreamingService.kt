package com.kvm404.phonecam

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.wifi.WifiManager
import android.os.Binder
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.util.Size
import android.view.OrientationEventListener
import androidx.camera.core.CameraSelector
import androidx.camera.core.ExperimentalGetImage
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.core.resolutionselector.ResolutionSelector
import androidx.camera.core.resolutionselector.ResolutionStrategy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleService
import com.kvm404.phonecam.pairing.PairingPayload
import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.VideoProfile
import com.kvm404.phonecam.pairing.deviceOrientationToSurfaceRotation
import com.kvm404.phonecam.pairing.quantizeOrientation
import com.kvm404.phonecam.streaming.CanvasComposer
import com.kvm404.phonecam.streaming.FrameConverter
import com.kvm404.phonecam.streaming.RtpPacketizer
import com.kvm404.phonecam.streaming.UdpRtpSender
import com.kvm404.phonecam.streaming.VideoEncoder
import java.net.InetSocketAddress
import java.util.concurrent.Executors

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

    fun clear() {
        payload = null
        rtpIdentity = null
        profile = null
    }
}

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
 * stop the stream, and observe start/stop through a [Callback]. Streaming is unaffected by
 * the activity binding, unbinding, or being destroyed; it stops only on the Leave button,
 * the notification Stop action, or an encoder/camera error.
 */
@ExperimentalGetImage
class StreamingService : LifecycleService() {

    /** Observes streaming lifecycle so a bound activity can reflect it in its UI. */
    interface Callback {
        /** Streaming is live (pipeline started successfully). */
        fun onStreamingStarted()

        /** Streaming ended. [error] is non-null on an encoder/camera failure. */
        fun onStreamingStopped(error: String?)
    }

    inner class LocalBinder : Binder() {
        val service: StreamingService get() = this@StreamingService
    }

    private val binder = LocalBinder()

    private val analysisExecutor = Executors.newSingleThreadExecutor()

    private var cameraProvider: ProcessCameraProvider? = null
    private var preview: Preview? = null
    private var imageAnalysis: ImageAnalysis? = null

    private var canvas: VideoProfile? = null
    private var payload: PairingPayload? = null
    private var rtpIdentity: RtpIdentity? = null
    private var videoEncoder: VideoEncoder? = null

    private var wifiLock: WifiManager.WifiLock? = null
    private var wakeLock: PowerManager.WakeLock? = null

    private var cameraSelector: CameraSelector = CameraSelector.DEFAULT_BACK_CAMERA

    /** When false, only ImageAnalysis is bound — the laptop stream is unchanged. */
    @Volatile
    private var previewWanted = true

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

    // ------------------------------------------------------------------ Service lifecycle

    override fun onCreate() {
        super.onCreate()
        isRunning = true
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        if (intent?.action == ACTION_STOP) {
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

    /**
     * Bind or drop the local Preview use case. ImageAnalysis (the encode path) stays bound,
     * so hiding the viewfinder does not pause the laptop stream.
     */
    fun setPreviewWanted(wanted: Boolean) {
        if (previewWanted == wanted) return
        previewWanted = wanted
        if (!streaming) return
        val provider = cameraProvider ?: return
        if (wanted) {
            enablePreviewUseCase(provider)
        } else {
            disablePreviewUseCase(provider)
        }
    }

    fun flipCamera() {
        cameraSelector = if (cameraSelector == CameraSelector.DEFAULT_BACK_CAMERA) {
            CameraSelector.DEFAULT_FRONT_CAMERA
        } else {
            CameraSelector.DEFAULT_BACK_CAMERA
        }
        if (streaming) bindCamera()
    }

    /** User-initiated stop (Leave button). Tears down and removes the service. */
    fun stopFromActivity() {
        stopStreaming(error = null)
    }

    // ------------------------------------------------------------------ Streaming pipeline

    private fun startStreaming() {
        val current = StreamingSession.payload
        val profile = StreamingSession.profile
        val rtp = StreamingSession.rtpIdentity
        val socket = rtp?.socket
        if (current == null || profile == null || rtp == null || socket == null) {
            // Nothing to stream (e.g. the session was cleared); shut down quietly.
            stopStreaming(error = null)
            return
        }
        payload = current
        canvas = profile
        rtpIdentity = rtp
        previewWanted = pendingPreviewWanted
        // Ownership of the session (and its socket) is now the service's.
        StreamingSession.clear()

        startForegroundNotification(current.name)

        val target = InetSocketAddress(current.rtpHost, current.rtpPort)
        val sender = UdpRtpSender(socket, target)
        val packetizer = RtpPacketizer(rtp.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        val encoder = VideoEncoder(profile, packetizer, sender) { error ->
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
        streaming = true
        bindCamera()
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
        stopOrientationTracking()
        orientationListener = null
        imageAnalysis?.clearAnalyzer()
        imageAnalysis = null
        preview = null
        cameraProvider?.unbindAll()
        videoEncoder?.stop()
        videoEncoder = null
        releaseLocks()
        rtpIdentity?.close()
        rtpIdentity = null
    }

    private fun bindCamera() {
        val provider = cameraProvider
        if (provider != null) {
            bindCamera(provider)
            return
        }
        val future = ProcessCameraProvider.getInstance(this)
        future.addListener({
            try {
                val resolved = future.get()
                cameraProvider = resolved
                if (streaming) bindCamera(resolved)
            } catch (e: Exception) {
                stopStreaming(error = e.localizedMessage ?: e.toString())
            }
        }, ContextCompat.getMainExecutor(this))
    }

    private fun bindCamera(provider: ProcessCameraProvider) {
        val analysis = buildAnalysis()
        // Seed the freshly-bound analyzer with the current device orientation so the first
        // frames (and any post-flip frames) are already upright before the sensor next fires.
        analysis.targetRotation = deviceOrientationToSurfaceRotation(deviceOrientation)
        imageAnalysis = analysis
        provider.unbindAll()
        preview = null
        try {
            if (previewWanted) {
                val previewUseCase = Preview.Builder().build()
                preview = previewUseCase
                provider.bindToLifecycle(this, cameraSelector, previewUseCase, analysis)
            } else {
                provider.bindToLifecycle(this, cameraSelector, analysis)
            }
        } catch (e: Exception) {
            stopStreaming(error = e.localizedMessage ?: e.toString())
        }
    }

    private fun enablePreviewUseCase(provider: ProcessCameraProvider) {
        if (preview != null) return
        val previewUseCase = Preview.Builder().build()
        try {
            provider.bindToLifecycle(this, cameraSelector, previewUseCase)
            preview = previewUseCase
        } catch (_: Exception) {
            preview = null
        }
    }

    private fun disablePreviewUseCase(provider: ProcessCameraProvider) {
        val existing = preview ?: return
        existing.surfaceProvider = null
        provider.unbind(existing)
        preview = null
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

    private fun startForegroundNotification(laptop: String) {
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

        /** Read once at [startStreaming] so the first bind can skip the viewfinder. */
        @Volatile
        var pendingPreviewWanted = true

        private const val CHANNEL_ID = "phonecam_streaming"
        private const val NOTIFICATION_ID = 1
        private const val ACTION_STOP = "com.kvm404.phonecam.action.STOP"

        /** Same intent as the notification Stop action; works before the activity has a binder. */
        fun stopIntent(context: Context): Intent =
            Intent(context, StreamingService::class.java).setAction(ACTION_STOP)

        /** Safety valve so a wedged stream can never hold the CPU forever. */
        private const val WAKE_LOCK_TIMEOUT_MS = 12L * 60L * 60L * 1000L
    }
}
