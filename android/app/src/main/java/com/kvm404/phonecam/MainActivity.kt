package com.kvm404.phonecam

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.util.Size
import android.view.View
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
 * Single-activity, three-screen UI (Home / Scan / Session) switched by [ScreenState]. All
 * protocol + streaming machinery ([PairingController], [RtpIdentity], [FrameConverter],
 * [VideoEncoder], …) is reused unchanged; this class is only the UI/orchestration seam.
 */
@ExperimentalGetImage
class MainActivity : AppCompatActivity() {

    private enum class ScreenState { HOME, SCAN, SESSION }

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

    private var screenState = ScreenState.HOME

    /** The payload of the active session; null on Home. */
    private var payload: PairingPayload? = null

    /** Latest pairing state; null in the "found — not connected" phase. */
    private var pairingState: PairingState? = null

    /** Options: default Back camera, Auto orientation. */
    private var cameraSelector: CameraSelector = CameraSelector.DEFAULT_BACK_CAMERA
    private var orientationMode: OrientationMode = OrientationMode.AUTO

    /**
     * Camera rotation (0/90/180/270) needed to make frames upright, captured from analyzed
     * frames during the scan phase. The activity is portrait-locked so this is constant; it
     * seeds the /pair dims before streaming starts.
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
                    ScreenState.SESSION -> teardownToHome(getString(R.string.home_status_disconnected))
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
        binding.connectButton.setOnClickListener { onConnectClicked() }
        binding.startButton.setOnClickListener { onStartCameraClicked() }
        binding.stopButton.setOnClickListener { onStopCameraClicked() }
        binding.scanAgainButton.setOnClickListener { onScanConnectClicked() }
        binding.disconnectButton.setOnClickListener {
            teardownToHome(getString(R.string.home_status_disconnected))
        }

        binding.cameraToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            cameraSelector = if (checkedId == R.id.cameraFront) {
                CameraSelector.DEFAULT_FRONT_CAMERA
            } else {
                CameraSelector.DEFAULT_BACK_CAMERA
            }
            // Rebind live so the switch takes effect while streaming (brief hiccup is fine).
            if (videoEncoder != null) bindStreamingCamera()
        }

        binding.orientationToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            orientationMode = when (checkedId) {
                R.id.orientationPortrait -> OrientationMode.PORTRAIT
                R.id.orientationLandscape -> OrientationMode.LANDSCAPE
                else -> OrientationMode.AUTO
            }
        }
    }

    // ------------------------------------------------------------------ Screen switching

    private fun showHome(status: String) {
        screenState = ScreenState.HOME
        binding.homeStatusText.text = status
        binding.homeContainer.visibility = View.VISIBLE
        binding.scanContainer.visibility = View.GONE
        binding.sessionContainer.visibility = View.GONE
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
        binding.sessionContainer.visibility = View.GONE
        binding.scanContainer.visibility = View.VISIBLE
        bindScanCamera()
    }

    private fun showSession() {
        screenState = ScreenState.SESSION
        binding.homeContainer.visibility = View.GONE
        binding.scanContainer.visibility = View.GONE
        binding.sessionContainer.visibility = View.VISIBLE
        updateSessionUi()
    }

    /** Full teardown of any active session/scan, then return to Home. */
    private fun teardownToHome(status: String) {
        pairingJob?.cancel()
        pairingJob = null
        handledPayload = false
        stopStreaming()
        unbindCamera()
        payload = null
        pairingState = null
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
                showSessionOrHomeError(e.localizedMessage ?: e.toString())
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
                showSessionOrHomeError(e.localizedMessage ?: e.toString())
            }
        }
    }

    /** Bind the streaming pipeline (selected camera) to the small session preview card. */
    private fun bindStreamingCamera() {
        withProvider { provider ->
            val preview = Preview.Builder().build().also {
                it.surfaceProvider = binding.sessionPreview.surfaceProvider
            }
            val analysis = buildAnalysis(::analyzeForStreaming)
            imageAnalysis = analysis
            provider.unbindAll()
            try {
                provider.bindToLifecycle(this, cameraSelector, preview, analysis)
            } catch (e: Exception) {
                showSessionOrHomeError(e.localizedMessage ?: e.toString())
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

    /** A valid QR was found: stop scanning, unbind the camera, and open the Session screen. */
    private fun onPayloadDetected(detected: PairingPayload) {
        imageAnalysis?.clearAnalyzer()
        unbindCamera()
        pairingJob?.cancel()
        pairingJob = null
        payload = detected
        pairingState = null
        showSession()
    }

    // ------------------------------------------------------------------ Pairing

    private fun onConnectClicked() {
        val current = payload ?: return
        // Guard rapid taps: don't launch a second handshake while one is running or done.
        if (pairingJob != null) return

        // Orientation determines the /pair dims, so it is now locked for this connection.
        binding.orientationToggle.isEnabled = false
        binding.orientationHelper.visibility = View.VISIBLE

        val committed = effectiveVideo(current.video, orientationMode, cameraRotationDegrees)
        pairingState = PairingState.Pairing
        updateSessionUi()

        pairingJob = lifecycleScope.launch {
            val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
            rtpIdentity = rtp
            val controller =
                PairingController(HttpControlClient(), phoneIdentity, rtp, committed.profile)
            launch {
                repeatOnLifecycle(Lifecycle.State.STARTED) {
                    controller.state.collect { state ->
                        pairingState = state
                        updateSessionUi()
                    }
                }
            }
            withContext(Dispatchers.IO) { controller.run(current) }
        }
    }

    // ------------------------------------------------------------------ Streaming

    private fun onStartCameraClicked() {
        val current = payload ?: return
        if (pairingState !is PairingState.Paired) return
        // Guard rapid taps.
        if (videoEncoder != null) return

        val rtp = rtpIdentity
        val socket = rtp?.socket
        if (rtp == null || socket == null) {
            showSessionOrHomeError("missing RTP socket")
            return
        }

        // Encoder dims match the /pair dims: computed from the same base + mode + rotation.
        val committed = effectiveVideo(current.video, orientationMode, cameraRotationDegrees)
        val profile = committed.profile

        val target = InetSocketAddress(current.rtpHost, current.rtpPort)
        val sender = UdpRtpSender(socket, target)
        val packetizer = RtpPacketizer(rtp.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        val encoder = VideoEncoder(profile, packetizer, sender) { error ->
            runOnUiThread { showSessionOrHomeError(error.localizedMessage ?: error.toString()) }
        }
        videoEncoder = encoder
        encoder.start()

        sizePreviewCard(profile.width, profile.height)
        bindStreamingCamera()
        updateSessionUi()
    }

    private fun onStopCameraClicked() {
        // Stop encoding + unbind the analyzer, but stay Connected so Start is available again.
        videoEncoder?.stop()
        videoEncoder = null
        unbindCamera()
        updateSessionUi()
    }

    /**
     * Streaming-mode analyzer: center-crop the landscape sensor buffer to the pre-rotation
     * target for the chosen orientation, rotate it onto the committed encoder dims, then feed
     * the encoder. Both dims and rotation come from [effectiveVideo] so front/back switches
     * (rotation re-read per frame) and forced-orientation modes stay consistent.
     */
    private fun analyzeForStreaming(imageProxy: ImageProxy) {
        try {
            val current = payload
            val degrees = imageProxy.imageInfo.rotationDegrees
            val eff: EffectiveVideo? =
                current?.let { effectiveVideo(it.video, orientationMode, degrees) }
            val rotation = eff?.rotationDegrees ?: 0
            // Pre-rotation crop target: undo the swap that [rotation] will re-apply.
            val cropWidth: Int
            val cropHeight: Int
            if (rotation == 90 || rotation == 270) {
                cropWidth = eff?.profile?.height ?: 0
                cropHeight = eff?.profile?.width ?: 0
            } else {
                cropWidth = eff?.profile?.width ?: 0
                cropHeight = eff?.profile?.height ?: 0
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
            runOnUiThread { showSessionOrHomeError(e.message ?: e.toString()) }
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
        rtpIdentity?.close()
        rtpIdentity = null
    }

    // ------------------------------------------------------------------ Session UI

    /** Render the Session screen from [payload], [pairingState] and the streaming flag. */
    private fun updateSessionUi() {
        if (screenState != ScreenState.SESSION) return
        val current = payload ?: return
        binding.sessionName.text = current.name

        val streaming = videoEncoder != null
        val state = pairingState

        // Default everything hidden, then reveal per state.
        binding.connectButton.visibility = View.GONE
        binding.startButton.visibility = View.GONE
        binding.stopButton.visibility = View.GONE
        binding.scanAgainButton.visibility = View.GONE
        binding.previewCard.visibility = View.GONE

        when (state) {
            null -> {
                binding.sessionStatus.text =
                    getString(R.string.session_status_found, current.name)
                binding.connectButton.visibility = View.VISIBLE
                binding.orientationToggle.isEnabled = true
                binding.orientationHelper.visibility = View.GONE
            }
            PairingState.Idle, PairingState.Pairing -> {
                binding.sessionStatus.text = getString(R.string.session_status_pairing)
            }
            PairingState.WaitingApproval -> {
                binding.sessionStatus.text =
                    getString(R.string.session_status_waiting, current.name)
            }
            is PairingState.Paired -> {
                if (streaming) {
                    val encDims = effectiveVideo(current.video, orientationMode, cameraRotationDegrees)
                    binding.sessionStatus.text = getString(
                        R.string.session_status_streaming,
                        encDims.profile.width,
                        encDims.profile.height,
                        encDims.profile.fps,
                    )
                    binding.stopButton.visibility = View.VISIBLE
                    binding.previewCard.visibility = View.VISIBLE
                } else {
                    binding.sessionStatus.text = getString(R.string.session_status_connected)
                    binding.startButton.visibility = View.VISIBLE
                }
            }
            is PairingState.Failed -> {
                binding.sessionStatus.text =
                    getString(R.string.session_status_failed, state.message)
                binding.scanAgainButton.visibility = View.VISIBLE
            }
        }
    }

    /** Size the preview card to ~half the screen width, matching the stream aspect ratio. */
    private fun sizePreviewCard(streamWidth: Int, streamHeight: Int) {
        if (streamWidth <= 0 || streamHeight <= 0) return
        val cardWidth = resources.displayMetrics.widthPixels / 2
        val cardHeight = (cardWidth.toLong() * streamHeight / streamWidth).toInt()
        val params = binding.sessionPreview.layoutParams
        params.width = cardWidth
        params.height = cardHeight
        binding.sessionPreview.layoutParams = params
    }

    private fun showSessionOrHomeError(message: String) {
        val text = getString(R.string.home_status_error, message)
        if (screenState == ScreenState.SESSION) {
            binding.sessionStatus.text = getString(R.string.session_status_error, message)
        } else {
            binding.homeStatusText.text = text
        }
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
