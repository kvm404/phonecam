package com.kvm404.phonecam

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Bundle
import android.util.Size
import android.view.OrientationEventListener
import android.view.View
import android.view.WindowManager
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.camera.core.CameraSelector
import androidx.camera.core.ExperimentalGetImage
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.core.resolutionselector.ResolutionSelector
import androidx.camera.core.resolutionselector.ResolutionStrategy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.google.mlkit.vision.barcode.BarcodeScannerOptions
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.common.InputImage
import com.kvm404.phonecam.databinding.ActivityMainBinding
import com.kvm404.phonecam.pairing.HttpControlClient
import com.kvm404.phonecam.pairing.PairingController
import com.kvm404.phonecam.pairing.PairingPayload
import com.kvm404.phonecam.pairing.PairingState
import com.kvm404.phonecam.pairing.PhoneIdentity
import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.VideoProfile
import com.kvm404.phonecam.pairing.deviceOrientationToSurfaceRotation
import com.kvm404.phonecam.pairing.quantizeOrientation
import com.kvm404.phonecam.streaming.CanvasComposer
import com.kvm404.phonecam.streaming.FrameConverter
import com.kvm404.phonecam.streaming.RtpPacketizer
import com.kvm404.phonecam.streaming.UdpRtpSender
import com.kvm404.phonecam.streaming.VideoEncoder
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.net.InetSocketAddress
import java.util.UUID
import java.util.concurrent.Executors

/**
 * Single-activity, three-screen UI (Home / Scan / Live) switched by [ScreenState]. All
 * protocol + streaming machinery ([PairingController], [RtpIdentity], [FrameConverter],
 * [VideoEncoder], …) is reused unchanged; this class is only the UI/orchestration seam.
 *
 * The flow is deliberately gmeet-like: a valid QR IMMEDIATELY pairs, and on approval the app
 * IMMEDIATELY starts streaming — no Connect / Start buttons in between.
 *
 * Orientation is gmeet-like too: the encoder and `/pair` dims are ALWAYS the payload's base
 * landscape profile (the "canvas") and are never swapped or renegotiated. Rotation is handled
 * per frame on the phone — a sensor-driven [OrientationEventListener] sets the ImageAnalysis
 * targetRotation so every frame's `rotationDegrees` is the correct upright rotation (including
 * reverse orientations), and [CanvasComposer] pillarboxes portrait content back onto the fixed
 * canvas. Because the dims never change, rotating mid-stream is instant and glitch-free.
 */
@ExperimentalGetImage
class MainActivity : AppCompatActivity() {

    private enum class ScreenState { HOME, SCAN, LIVE }

    private lateinit var binding: ActivityMainBinding

    private val analysisExecutor = Executors.newSingleThreadExecutor()
    private val barcodeScanner by lazy {
        BarcodeScanning.getClient(
            BarcodeScannerOptions.Builder()
                .setBarcodeFormats(Barcode.FORMAT_QR_CODE)
                .build()
        )
    }

    private var cameraProvider: ProcessCameraProvider? = null
    private var imageAnalysis: ImageAnalysis? = null
    private var pairingJob: Job? = null

    /** RTP identity (SSRC + open source socket) committed during the current pairing. */
    private var rtpIdentity: RtpIdentity? = null

    /** Live H.264 encoder while streaming; null when idle or scanning. */
    private var videoEncoder: VideoEncoder? = null

    /** Low-latency Wi-Fi lock held only while streaming; see [acquireStreamingLocks]. */
    private var wifiLock: WifiManager.WifiLock? = null

    private var screenState = ScreenState.HOME

    /** The payload of the active session; null on Home. */
    private var payload: PairingPayload? = null

    /**
     * The fixed canvas profile: the payload's base landscape dims, announced verbatim at
     * `/pair` and fed to the encoder. Never swapped or renegotiated — all rotation handling is
     * per-frame composition onto these dims. Fixed for the life of the connection.
     */
    private var committedProfile: VideoProfile? = null

    /** Selected camera; flips between back/front mid-stream via the flip button. */
    private var cameraSelector: CameraSelector = CameraSelector.DEFAULT_BACK_CAMERA

    /**
     * Current quantized device orientation (0/90/180/270), driven by [orientationListener]
     * while streaming. Read to seed a freshly-bound analyzer's targetRotation.
     */
    @Volatile
    private var deviceOrientation = 0

    /** Sensor listener active only while streaming; drives the analyzer targetRotation. */
    private var orientationListener: OrientationEventListener? = null

    /** Guards so only the first successfully-parsed payload triggers a session. */
    @Volatile
    private var handledPayload = false

    private val phoneIdentity: PhoneIdentity by lazy {
        val prefs = getSharedPreferences("phonecam", MODE_PRIVATE)
        val id = prefs.getString("phone_id", null)
            ?: UUID.randomUUID().toString().also { prefs.edit().putString("phone_id", it).apply() }
        PhoneIdentity(id = id, name = Build.MODEL)
    }

    private val requestCameraPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) {
                showScan()
            } else {
                binding.homeStatusText.text = getString(R.string.home_status_permission_needed)
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        wireUp()
        showHome(getString(R.string.home_status_not_connected))

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                when (screenState) {
                    ScreenState.SCAN -> teardownToHome(getString(R.string.home_status_not_connected))
                    ScreenState.LIVE -> teardownToHome(getString(R.string.home_status_disconnected))
                    ScreenState.HOME -> finish()
                }
            }
        })
    }

    private fun wireUp() {
        binding.scanConnectButton.setOnClickListener { onScanConnectClicked() }
        binding.exitButton.setOnClickListener { finish() }
        binding.scanCancelButton.setOnClickListener {
            teardownToHome(getString(R.string.home_status_not_connected))
        }
        binding.leaveButton.setOnClickListener {
            teardownToHome(getString(R.string.home_status_disconnected))
        }
        binding.flipCameraButton.setOnClickListener {
            cameraSelector = if (cameraSelector == CameraSelector.DEFAULT_BACK_CAMERA) {
                CameraSelector.DEFAULT_FRONT_CAMERA
            } else {
                CameraSelector.DEFAULT_BACK_CAMERA
            }
            // Rebind live so the switch takes effect while streaming (brief hiccup is fine).
            if (videoEncoder != null) bindStreamingCamera()
        }
    }

    // ------------------------------------------------------------------ Screen switching

    private fun showHome(status: String) {
        screenState = ScreenState.HOME
        binding.homeStatusText.text = status
        binding.homeContainer.visibility = View.VISIBLE
        binding.scanContainer.visibility = View.GONE
        binding.liveContainer.visibility = View.GONE
    }

    private fun onScanConnectClicked() {
        if (hasCameraPermission()) {
            showScan()
        } else {
            requestCameraPermission.launch(Manifest.permission.CAMERA)
        }
    }

    private fun showScan() {
        screenState = ScreenState.SCAN
        handledPayload = false
        binding.homeContainer.visibility = View.GONE
        binding.liveContainer.visibility = View.GONE
        binding.scanContainer.visibility = View.VISIBLE
        bindScanCamera()
    }

    /** Enter the Live screen in its "connecting" state; pairing/streaming take over from here. */
    private fun showLive() {
        screenState = ScreenState.LIVE
        binding.homeContainer.visibility = View.GONE
        binding.scanContainer.visibility = View.GONE
        binding.liveContainer.visibility = View.VISIBLE
        updateLiveUi()
    }

    /** Full teardown of any active session/scan, then return to Home. */
    private fun teardownToHome(status: String) {
        pairingJob?.cancel()
        pairingJob = null
        handledPayload = false
        stopStreaming()
        unbindCamera()
        payload = null
        committedProfile = null
        showHome(status)
    }

    private fun hasCameraPermission(): Boolean =
        ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) ==
            PackageManager.PERMISSION_GRANTED

    // ------------------------------------------------------------------ Camera binding

    private fun withProvider(action: (ProcessCameraProvider) -> Unit) {
        val existing = cameraProvider
        if (existing != null) {
            action(existing)
            return
        }
        val future = ProcessCameraProvider.getInstance(this)
        future.addListener({
            try {
                val provider = future.get()
                cameraProvider = provider
                action(provider)
            } catch (e: Exception) {
                failToHome(e.localizedMessage ?: e.toString())
            }
        }, ContextCompat.getMainExecutor(this))
    }

    private fun buildAnalysis(analyzer: (ImageProxy) -> Unit): ImageAnalysis {
        val resolutionSelector = ResolutionSelector.Builder()
            .setResolutionStrategy(
                ResolutionStrategy(
                    Size(1280, 720),
                    ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
                )
            )
            .build()
        return ImageAnalysis.Builder()
            .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
            .setResolutionSelector(resolutionSelector)
            .build()
            .also { it.setAnalyzer(analysisExecutor, analyzer) }
    }

    /** Bind the QR-scanning pipeline (back camera) to the fullscreen scan preview. */
    private fun bindScanCamera() {
        withProvider { provider ->
            val preview = Preview.Builder().build().also {
                it.surfaceProvider = binding.scanPreview.surfaceProvider
            }
            val analysis = buildAnalysis(::analyze)
            imageAnalysis = analysis
            provider.unbindAll()
            try {
                provider.bindToLifecycle(this, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis)
            } catch (e: Exception) {
                failToHome(e.localizedMessage ?: e.toString())
            }
        }
    }

    /** Bind the streaming pipeline (selected camera) to the small live preview card. */
    private fun bindStreamingCamera() {
        withProvider { provider ->
            val preview = Preview.Builder().build().also {
                it.surfaceProvider = binding.livePreview.surfaceProvider
            }
            val analysis = buildAnalysis(::analyzeForStreaming)
            // Seed the freshly-bound analyzer with the current device orientation so the first
            // frames (and any post-flip frames) are already upright before the sensor next fires.
            analysis.targetRotation = deviceOrientationToSurfaceRotation(deviceOrientation)
            imageAnalysis = analysis
            provider.unbindAll()
            try {
                provider.bindToLifecycle(this, cameraSelector, preview, analysis)
            } catch (e: Exception) {
                failToHome(e.localizedMessage ?: e.toString())
            }
        }
    }

    private fun unbindCamera() {
        imageAnalysis?.clearAnalyzer()
        imageAnalysis = null
        cameraProvider?.unbindAll()
    }

    // ------------------------------------------------------------------ Scan / detect

    private fun analyze(imageProxy: ImageProxy) {
        val mediaImage = imageProxy.image
        if (mediaImage == null || handledPayload) {
            imageProxy.close()
            return
        }
        val image = InputImage.fromMediaImage(mediaImage, imageProxy.imageInfo.rotationDegrees)
        barcodeScanner.process(image)
            .addOnSuccessListener { barcodes -> handleBarcodes(barcodes) }
            .addOnCompleteListener { imageProxy.close() }
    }

    private fun handleBarcodes(barcodes: List<Barcode>) {
        if (handledPayload) return
        for (barcode in barcodes) {
            val raw = barcode.rawValue ?: continue
            val parsed = try {
                PairingPayload.parse(raw)
            } catch (e: IllegalArgumentException) {
                // Not a pairing QR (or malformed) — keep scanning.
                continue
            }
            handledPayload = true
            onPayloadDetected(parsed)
            return
        }
    }

    /**
     * A valid QR was found: stop scanning, unbind the camera, open the Live screen, and
     * IMMEDIATELY begin pairing — no button in between.
     */
    private fun onPayloadDetected(detected: PairingPayload) {
        imageAnalysis?.clearAnalyzer()
        unbindCamera()
        pairingJob?.cancel()
        pairingJob = null
        payload = detected
        showLive()
        startPairing()
    }

    // ------------------------------------------------------------------ Pairing

    /**
     * Kick off the handshake straight away. The encoder/`/pair` dims are the payload's base
     * landscape canvas verbatim — never swapped — and on [PairingState.Paired] streaming starts
     * automatically. Rotation is handled per frame, so nothing about orientation is committed here.
     */
    private fun startPairing() {
        val current = payload ?: return
        // Guard rapid QR re-detections: don't launch a second handshake while one is running.
        if (pairingJob != null) return

        val committed = current.video
        committedProfile = committed
        updateLiveUi()

        pairingJob = lifecycleScope.launch {
            val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
            rtpIdentity = rtp
            val controller =
                PairingController(HttpControlClient(), phoneIdentity, rtp, committed)
            launch {
                repeatOnLifecycle(Lifecycle.State.STARTED) {
                    controller.state.collect { state ->
                        when (state) {
                            is PairingState.Paired -> startStreaming()
                            is PairingState.Failed -> failToHome(state.message)
                            else -> updateLiveUi()
                        }
                    }
                }
            }
            withContext(Dispatchers.IO) { controller.run(current) }
        }
    }

    // ------------------------------------------------------------------ Streaming

    /** Auto-start: called on [PairingState.Paired]. Idempotent against rapid re-emission. */
    private fun startStreaming() {
        val current = payload ?: return
        val profile = committedProfile ?: return
        // Guard double-start.
        if (videoEncoder != null) return

        val rtp = rtpIdentity
        val socket = rtp?.socket
        if (rtp == null || socket == null) {
            failToHome("missing RTP socket")
            return
        }

        val target = InetSocketAddress(current.rtpHost, current.rtpPort)
        val sender = UdpRtpSender(socket, target)
        val packetizer = RtpPacketizer(rtp.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        val encoder = VideoEncoder(profile, packetizer, sender) { error ->
            runOnUiThread { failToHome(error.localizedMessage ?: error.toString()) }
        }
        videoEncoder = encoder
        encoder.start()

        acquireStreamingLocks()
        startOrientationTracking()
        sizePreviewCard(profile.width, profile.height)
        bindStreamingCamera()
        updateLiveUi()
    }

    /**
     * Track the physical device orientation while streaming. A UI rotation is not enough:
     * Android will not report reverse-portrait, and `configChanges` masks some transitions.
     * The sensor value is quantized to 0/90/180/270 and mapped to the ImageAnalysis
     * targetRotation, which makes each frame's `rotationDegrees` the correct upright rotation.
     */
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

    /**
     * Held only while streaming: keep the screen on (the user's stream must not die to the
     * lock screen) and hold a low-latency Wi-Fi lock. Without the lock, Android's periodic
     * Wi-Fi power-save/background-scan cycle takes the radio off-channel every ~10s, dropping
     * an RTP burst and freezing the meeting feed until the next keyframe.
     */
    private fun acquireStreamingLocks() {
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
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
    }

    private fun releaseStreamingLocks() {
        window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        wifiLock?.takeIf { it.isHeld }?.release()
    }

    /**
     * Streaming-mode analyzer, fixed-canvas pipeline. Uniform for every rotation:
     *  1. center-crop the landscape sensor buffer to the canvas dims (undoes a wider sensor,
     *     e.g. 1600x720 -> 1280x720);
     *  2. rotate by the sensor-correct `rotationDegrees` — 0/180 keep canvas dims, 90/270 swap
     *     to portrait (720x1280);
     *  3. compose back onto the fixed canvas — a no-op for landscape, a centered pillarbox for
     *     portrait — so the encoder always receives exactly the canvas dims.
     *
     * The encoder/`/pair` dims never change, so rotating mid-stream (including reverse
     * orientations) is instant and needs no renegotiation. Front/back flips re-read the
     * rotation the same way.
     */
    private fun analyzeForStreaming(imageProxy: ImageProxy) {
        try {
            val canvas = committedProfile ?: return
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
            // Unrecoverable geometry problem (frame smaller than the encoder target):
            // surface it instead of silently dropping every frame.
            runOnUiThread { failToHome(e.message ?: e.toString()) }
        } catch (_: Exception) {
            // Drop this frame; a transient conversion/encode error must not stop the stream.
        } finally {
            imageProxy.close()
        }
    }

    /** Stop and release the encoder and the pairing socket, if any. */
    private fun stopStreaming() {
        videoEncoder?.stop()
        videoEncoder = null
        stopOrientationTracking()
        releaseStreamingLocks()
        rtpIdentity?.close()
        rtpIdentity = null
    }

    // ------------------------------------------------------------------ Live UI

    /** Render the Live screen: "connecting" until streaming, then "Live — <laptop>". */
    private fun updateLiveUi() {
        if (screenState != ScreenState.LIVE) return
        val current = payload ?: return
        val streaming = videoEncoder != null
        if (streaming) {
            binding.liveStatus.text = getString(R.string.live_status, current.name)
            binding.previewCard.visibility = View.VISIBLE
            binding.flipCameraButton.visibility = View.VISIBLE
            binding.leaveButton.visibility = View.VISIBLE
        } else {
            binding.liveStatus.text = getString(R.string.live_connecting, current.name)
            binding.previewCard.visibility = View.GONE
            binding.flipCameraButton.visibility = View.GONE
            binding.leaveButton.visibility = View.GONE
        }
    }

    /** Size the preview card to ~half the screen width, matching the committed aspect ratio. */
    private fun sizePreviewCard(streamWidth: Int, streamHeight: Int) {
        if (streamWidth <= 0 || streamHeight <= 0) return
        val cardWidth = resources.displayMetrics.widthPixels / 2
        val cardHeight = (cardWidth.toLong() * streamHeight / streamWidth).toInt()
        val params = binding.livePreview.layoutParams
        params.width = cardWidth
        params.height = cardHeight
        binding.livePreview.layoutParams = params
    }

    /** Any pairing/streaming failure: surface the reason on Home's status card and return Home. */
    private fun failToHome(message: String) {
        teardownToHome(getString(R.string.home_status_error, message))
    }

    override fun onDestroy() {
        super.onDestroy()
        pairingJob?.cancel()
        stopStreaming()
        orientationListener?.disable()
        imageAnalysis?.clearAnalyzer()
        cameraProvider?.unbindAll()
        analysisExecutor.shutdown()
        barcodeScanner.close()
    }
}
