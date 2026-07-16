package com.kvm404.phonecam

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
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
import android.util.Size
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

@ExperimentalGetImage
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    private val analysisExecutor = Executors.newSingleThreadExecutor()
    private val barcodeScanner by lazy {
        BarcodeScanning.getClient(
            BarcodeScannerOptions.Builder()
                .setBarcodeFormats(Barcode.FORMAT_QR_CODE)
                .build()
        )
    }

    private var imageAnalysis: ImageAnalysis? = null
    private var pairingJob: Job? = null

    /** RTP identity (SSRC + open source socket) committed during the current pairing. */
    private var rtpIdentity: RtpIdentity? = null

    /** Live H.264 encoder while streaming; null when idle or scanning. */
    private var videoEncoder: VideoEncoder? = null

    /** Encoder target profile (rotated dims); frames are cropped then rotated to this. */
    private var streamingProfile: VideoProfile? = null

    /**
     * Camera rotation (0/90/180/270) needed to make frames upright, captured from analyzed
     * frames. The activity is locked portrait so this is constant, but reading it per frame
     * during QR scanning makes the value available before streaming starts (i.e. at /pair).
     */
    @Volatile
    private var cameraRotationDegrees = 0

    /** Guards so only the first successfully-parsed payload triggers pairing. */
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
                startCamera()
            } else {
                binding.statusText.text = getString(R.string.status_permission_needed)
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.statusText.setOnClickListener { resetScanning() }

        if (hasCameraPermission()) {
            startCamera()
        } else {
            binding.statusText.text = getString(R.string.status_permission_needed)
            requestCameraPermission.launch(Manifest.permission.CAMERA)
        }
    }

    private fun hasCameraPermission(): Boolean =
        ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) ==
            PackageManager.PERMISSION_GRANTED

    private fun startCamera() {
        val providerFuture = ProcessCameraProvider.getInstance(this)
        providerFuture.addListener({
            try {
                val cameraProvider = providerFuture.get()
                val preview = Preview.Builder().build().also {
                    it.surfaceProvider = binding.previewView.surfaceProvider
                }
                val resolutionSelector = ResolutionSelector.Builder()
                    .setResolutionStrategy(
                        ResolutionStrategy(
                            Size(1280, 720),
                            ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
                        )
                    )
                    .build()
                val analysis = ImageAnalysis.Builder()
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                    .setResolutionSelector(resolutionSelector)
                    .build()
                    .also { it.setAnalyzer(analysisExecutor, ::analyze) }
                imageAnalysis = analysis

                cameraProvider.unbindAll()
                cameraProvider.bindToLifecycle(
                    this,
                    CameraSelector.DEFAULT_BACK_CAMERA,
                    preview,
                    analysis
                )
                binding.statusText.text = getString(R.string.status_scan_qr)
            } catch (e: Exception) {
                binding.statusText.text =
                    getString(R.string.status_error, e.localizedMessage ?: e.toString())
            }
        }, ContextCompat.getMainExecutor(this))
    }

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
            val payload = try {
                PairingPayload.parse(raw)
            } catch (e: IllegalArgumentException) {
                // Not a pairing QR (or malformed) — keep scanning.
                continue
            }
            handledPayload = true
            onPayloadDetected(payload)
            return
        }
    }

    private fun onPayloadDetected(payload: PairingPayload) {
        imageAnalysis?.clearAnalyzer()
        pairingJob?.cancel()
        val effectiveVideo = effectiveProfile(payload.video, cameraRotationDegrees)
        pairingJob = lifecycleScope.launch {
            val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
            rtpIdentity = rtp
            val controller =
                PairingController(HttpControlClient(), phoneIdentity, rtp, effectiveVideo)
            launch {
                repeatOnLifecycle(Lifecycle.State.STARTED) {
                    controller.state.collect { render(it, payload) }
                }
            }
            withContext(Dispatchers.IO) { controller.run(payload) }
        }
    }

    private fun render(state: PairingState, payload: PairingPayload) {
        when (state) {
            PairingState.Idle -> binding.statusText.text = getString(R.string.status_scan_qr)
            PairingState.Pairing ->
                binding.statusText.text = getString(R.string.status_pairing, payload.name)
            PairingState.WaitingApproval ->
                binding.statusText.text =
                    getString(R.string.status_waiting_approval, payload.name)
            is PairingState.Paired -> startStreaming(state.payload)
            is PairingState.Failed ->
                binding.statusText.text = getString(R.string.status_failed, state.message)
        }
    }

    /**
     * Once paired, swap the QR analyzer for the streaming analyzer and start the H.264
     * encoder, sending RTP from the pairing-committed socket + SSRC to the payload's RTP
     * endpoint at its video profile. Idempotent: subsequent Paired emissions are ignored.
     */
    private fun startStreaming(payload: PairingPayload) {
        if (videoEncoder != null) return
        val rtp = rtpIdentity
        val socket = rtp?.socket
        if (rtp == null || socket == null) {
            binding.statusText.text =
                getString(R.string.status_error, "missing RTP socket")
            return
        }

        // The encoder profile is the rotated (possibly portrait) profile — the same dims
        // reported to the laptop at /pair — so it must match what we crop+rotate to.
        val profile = effectiveProfile(payload.video, cameraRotationDegrees)

        val target = InetSocketAddress(payload.rtpHost, payload.rtpPort)
        val sender = UdpRtpSender(socket, target)
        val packetizer = RtpPacketizer(rtp.ssrc, RtpPacketizer.randomInitialSequenceNumber())
        val encoder = VideoEncoder(profile, packetizer, sender) { error ->
            runOnUiThread {
                binding.statusText.text =
                    getString(R.string.status_error, error.localizedMessage ?: error.toString())
            }
        }
        videoEncoder = encoder
        streamingProfile = profile
        encoder.start()

        imageAnalysis?.setAnalyzer(analysisExecutor, ::analyzeForStreaming)
        binding.statusText.text = getString(
            R.string.status_streaming,
            "${payload.rtpHost}:${payload.rtpPort}",
            profile.width,
            profile.height,
            profile.fps,
        )
    }

    /**
     * The encoder's actual output profile: [base] with width/height swapped when the camera
     * needs a 90/270° rotation to display upright, so streamed dims are portrait (e.g. the
     * laptop's 1280x720 profile becomes 720x1280). fps is unchanged.
     */
    private fun effectiveProfile(base: VideoProfile, rotation: Int): VideoProfile =
        if (rotation == 90 || rotation == 270) {
            VideoProfile(width = base.height, height = base.width, fps = base.fps)
        } else {
            base
        }

    /**
     * Streaming-mode analyzer: center-crop the landscape sensor buffer to the PRE-rotation
     * target, rotate to the encoder's (possibly portrait) dims, then hand it to the encoder.
     *
     * Order matters: [streamingProfile] holds the rotated encoder dims. For a 90/270 rotation
     * we crop the source to those dims SWAPPED (landscape, e.g. 1280x720) and then rotate to
     * land on the encoder profile (720x1280). For 0/180 the crop target is the encoder dims
     * directly. rotationDegrees is read per frame (cheap; constant in practice).
     */
    private fun analyzeForStreaming(imageProxy: ImageProxy) {
        try {
            val profile = streamingProfile
            val degrees = imageProxy.imageInfo.rotationDegrees
            // Pre-rotation crop target: undo the width/height swap that rotation will re-apply.
            val cropWidth: Int
            val cropHeight: Int
            if (degrees == 90 || degrees == 270) {
                cropWidth = profile?.height ?: 0
                cropHeight = profile?.width ?: 0
            } else {
                cropWidth = profile?.width ?: 0
                cropHeight = profile?.height ?: 0
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
            val frame = FrameConverter.rotate(cropped, degrees)
            videoEncoder?.encode(frame)
        } catch (e: IllegalArgumentException) {
            // Unrecoverable geometry problem (frame smaller than the encoder target):
            // surface it instead of silently dropping every frame.
            runOnUiThread {
                binding.statusText.text =
                    getString(R.string.status_error, e.message ?: e.toString())
            }
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
        streamingProfile = null
        rtpIdentity?.close()
        rtpIdentity = null
    }

    /** Cancel any in-flight pairing and resume QR scanning. Driven by tapping the status text. */
    private fun resetScanning() {
        pairingJob?.cancel()
        pairingJob = null
        handledPayload = false
        stopStreaming()
        imageAnalysis?.setAnalyzer(analysisExecutor, ::analyze)
        binding.statusText.text = getString(R.string.status_scan_qr)
    }

    override fun onDestroy() {
        super.onDestroy()
        pairingJob?.cancel()
        stopStreaming()
        imageAnalysis?.clearAnalyzer()
        analysisExecutor.shutdown()
        barcodeScanner.close()
    }
}
