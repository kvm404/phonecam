package com.kvm404.phonecam

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Bundle
import android.util.Size
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
import com.kvm404.phonecam.pairing.EffectiveVideo
import com.kvm404.phonecam.pairing.HttpControlClient
import com.kvm404.phonecam.pairing.OrientationMode
import com.kvm404.phonecam.pairing.PairingController
import com.kvm404.phonecam.pairing.PairingPayload
import com.kvm404.phonecam.pairing.PairingState
import com.kvm404.phonecam.pairing.PhoneIdentity
import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.effectiveVideo
import com.kvm404.phonecam.pairing.frameRotation
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
 * IMMEDIATELY starts streaming — no Connect / Start buttons in between. Orientation is
 * automatic: the encoder dims are committed at `/pair` from the rotation observed while
 * scanning, and the activity itself rotates with the device (see `configChanges` in the
 * manifest) so the live stream survives rotation instead of being destroyed by a recreate.
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
     * The encoder target committed at `/pair`: dims announced to the laptop and the rotation
     * that made the scan-time buffer upright. Fixed for the life of the connection; the
     * per-frame rotation is re-derived against it by [frameRotation] as the device rotates.
     */
    private var committedVideo: EffectiveVideo? = null

    /** Selected camera; flips between back/front mid-stream via the flip button. */
    private var cameraSelector: CameraSelector = CameraSelector.DEFAULT_BACK_CAMERA

    /**
     * Camera rotation (0/90/180/270) needed to make frames upright, captured from analyzed
     * frames during the scan phase. Whatever orientation the phone is held in while scanning
     * seeds the committed `/pair` dims — this is the "automatic" orientation.
     */
    @Volatile
    private var cameraRotationDegrees = 0

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
        committedVideo = null
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
        cameraRotationDegrees = imageProxy.imageInfo.rotationDegrees
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
     * Kick off the handshake straight away. The encoder dims are committed here from the
     * scan-time rotation, and on [PairingState.Paired] streaming starts automatically.
     */
    private fun startPairing() {
        val current = payload ?: return
        // Guard rapid QR re-detections: don't launch a second handshake while one is running.
        if (pairingJob != null) return

        val committed = effectiveVideo(current.video, OrientationMode.AUTO, cameraRotationDegrees)
        committedVideo = committed
        updateLiveUi()

        pairingJob = lifecycleScope.launch {
            val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
            rtpIdentity = rtp
            val controller =
                PairingController(HttpControlClient(), phoneIdentity, rtp, committed.profile)
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
        val committed = committedVideo ?: return
        // Guard double-start.
        if (videoEncoder != null) return

        val rtp = rtpIdentity
        val socket = rtp?.socket
        if (rtp == null || socket == null) {
            failToHome("missing RTP socket")
            return
        }

        val profile = committed.profile
        val target = InetSocketAddress(current.rtpHost, current.rtpPort)
        val sender = UdpRtpSender(socket, target)
        val packetizer = RtpPacketizer(rtp.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        val encoder = VideoEncoder(profile, packetizer, sender) { error ->
            runOnUiThread { failToHome(error.localizedMessage ?: error.toString()) }
        }
        videoEncoder = encoder
        encoder.start()

        acquireStreamingLocks()
        sizePreviewCard(profile.width, profile.height)
        bindStreamingCamera()
        updateLiveUi()
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
     * Streaming-mode analyzer: center-crop the landscape sensor buffer to the pre-rotation
     * target for the committed encoder dims, rotate it onto those dims, then feed the encoder.
     *
     * The rotation is re-derived per frame by [frameRotation] against the committed profile, so
     * the picture stays upright across 180° flips and same-class rotations, and falls back to
     * the committed rotation (keeping the committed orientation) when the device is turned 90°
     * into the other orientation class. Front/back switches re-read the rotation the same way.
     */
    private fun analyzeForStreaming(imageProxy: ImageProxy) {
        try {
            val current = payload ?: return
            val committed = committedVideo ?: return
            val degrees = imageProxy.imageInfo.rotationDegrees
            val rotation = frameRotation(current.video, committed, degrees)
            // Pre-rotation crop target: undo the swap that [rotation] will re-apply.
            val cropWidth: Int
            val cropHeight: Int
            if (rotation == 90 || rotation == 270) {
                cropWidth = committed.profile.height
                cropHeight = committed.profile.width
            } else {
                cropWidth = committed.profile.width
                cropHeight = committed.profile.height
            }
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
                targetWidth = cropWidth,
                targetHeight = cropHeight,
            )
            val frame = FrameConverter.rotate(cropped, rotation)
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
        imageAnalysis?.clearAnalyzer()
        cameraProvider?.unbindAll()
        analysisExecutor.shutdown()
        barcodeScanner.close()
    }
}
