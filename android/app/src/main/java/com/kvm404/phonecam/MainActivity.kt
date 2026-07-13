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
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
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
                val analysis = ImageAnalysis.Builder()
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
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
        pairingJob = lifecycleScope.launch {
            val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
            val controller = PairingController(HttpControlClient(), phoneIdentity, rtp)
            launch {
                repeatOnLifecycle(Lifecycle.State.STARTED) {
                    controller.state.collect { render(it, payload) }
                }
            }
            withContext(Dispatchers.IO) { controller.run(payload) }
        }
    }

    private fun render(state: PairingState, payload: PairingPayload) {
        binding.statusText.text = when (state) {
            PairingState.Idle -> getString(R.string.status_scan_qr)
            PairingState.Pairing -> getString(R.string.status_pairing, payload.name)
            PairingState.WaitingApproval ->
                getString(R.string.status_waiting_approval, payload.name)
            is PairingState.Paired ->
                getString(R.string.status_paired, state.payload.name, state.payload.rtp)
            is PairingState.Failed -> getString(R.string.status_failed, state.message)
        }
    }

    /** Cancel any in-flight pairing and resume QR scanning. Driven by tapping the status text. */
    private fun resetScanning() {
        pairingJob?.cancel()
        pairingJob = null
        handledPayload = false
        imageAnalysis?.setAnalyzer(analysisExecutor, ::analyze)
        binding.statusText.text = getString(R.string.status_scan_qr)
    }

    override fun onDestroy() {
        super.onDestroy()
        pairingJob?.cancel()
        imageAnalysis?.clearAnalyzer()
        analysisExecutor.shutdown()
        barcodeScanner.close()
    }
}
