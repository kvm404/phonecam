package com.kvm404.phonecam

import android.Manifest
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.graphics.drawable.Drawable
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import android.os.PowerManager
import android.util.Size
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.res.ResourcesCompat
import androidx.core.graphics.drawable.DrawableCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
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
import com.google.android.material.button.MaterialButton
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.kvm404.phonecam.pairing.CameraFacing
import com.kvm404.phonecam.pairing.HomeReconnect
import com.kvm404.phonecam.pairing.HomeReconnectResult
import com.kvm404.phonecam.pairing.HttpControlClient
import com.kvm404.phonecam.pairing.PairingController
import com.kvm404.phonecam.pairing.PairingPayload
import com.kvm404.phonecam.pairing.PairingState
import com.kvm404.phonecam.pairing.PhoneIdentity
import com.kvm404.phonecam.pairing.RtpIdentity
import com.kvm404.phonecam.pairing.StreamHealth
import com.kvm404.phonecam.pairing.StreamQuality
import com.kvm404.phonecam.pairing.TrustedLaptop
import com.kvm404.phonecam.pairing.TrustedLaptops
import com.kvm404.phonecam.pairing.VideoProfile
import com.kvm404.phonecam.streaming.ZoomStepper
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.time.Instant
import java.util.UUID
import java.util.concurrent.Executors

/**
 * Single-activity, three-screen UI (Home / Scan / Live) switched by [ScreenState]. This class
 * owns the scan + pairing flow only; once pairing succeeds the streaming pipeline runs in
 * [StreamingService] (a camera foreground service) so it survives the screen turning off, the
 * lock screen, and this activity being destroyed.
 *
 * The flow is deliberately gmeet-like: a valid QR IMMEDIATELY pairs, and on approval the app
 * IMMEDIATELY starts streaming — no Connect / Start buttons in between. On [PairingState.Paired]
 * the activity hands the payload + [RtpIdentity] + [VideoProfile] to the service (via
 * [StreamingSession]) and starts + binds it, then renders the Live screen from the service's
 * state (bound connection): status, preview hookup, flip and Leave all delegate to the service.
 *
 * If the activity is recreated or reopened while the service is still streaming, it rebinds and
 * shows the Live screen (tapping the app icon returns to the live session).
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

    /** Scan-only use cases; unbound individually (the provider is a process-wide singleton
     * shared with the service, so [ProcessCameraProvider.unbindAll] must not be used here). */
    private var scanPreview: Preview? = null
    private var imageAnalysis: ImageAnalysis? = null
    private var pairingJob: Job? = null

    /**
     * RTP identity committed during the current pairing. Owned by the activity only until
     * [PairingState.Paired]; ownership (and its open socket) then transfers to the service and
     * this reference is nulled. Non-null here means pairing is still in flight or has failed.
     */
    private var rtpIdentity: RtpIdentity? = null

    private var screenState = ScreenState.HOME

    /**
     * The quality preset chosen on Home and persisted in the "phonecam" prefs. Read/reflected
     * on Home, written the instant the user changes the toggle, and captured verbatim at scan
     * time as the canonical session resolution (overriding the QR payload's `video` dims).
     */
    private var selectedQuality: StreamQuality = StreamQuality.DEFAULT

    /** The payload of the active session; null on Home. Drives the pairing/connecting UI. */
    private var payload: PairingPayload? = null

    /**
     * The fixed canvas profile: the chosen [selectedQuality]'s landscape dims (overriding the
     * QR payload's `video`), announced verbatim at `/pair` and handed to the service's encoder.
     * Never swapped or renegotiated.
     */
    private var committedProfile: VideoProfile? = null

    /** Guards so only the first successfully-parsed payload triggers a session. */
    @Volatile
    private var handledPayload = false

    // --- Streaming service binding ------------------------------------------------------

    private var streamingService: StreamingService? = null
    private var bound = false

    /**
     * Set when the user hits Leave/Cancel (or Back on Live). [onPaired] starts the
     * foreground service and binds asynchronously; if Cancel wins that race,
     * [streamingService] is still null and [leaveSession] must stop via
     * [StreamingService.stopIntent] plus this latch so [onServiceConnected] cannot
     * attach a callback and keep the session alive.
     */
    @Volatile
    private var leaveRequested = false

    private var stopWatchJob: Job? = null
    @Volatile
    private var waitingToStartService = false

    private var previewAspectWidth = 16
    private var previewAspectHeight = 9

    private val serviceConnection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, service: IBinder?) {
            val svc = (service as StreamingService.LocalBinder).service
            streamingService = svc
            if (leaveRequested) {
                svc.setCallback(null)
                svc.stopFromActivity()
                return
            }
            svc.setCallback(streamingCallback)
            // Adopt the session details (name/profile) the service holds, so a rebind after an
            // activity recreate can render the Live screen without the original payload.
            svc.profile()?.let { committedProfile = it }
            updateLiveUi()
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            streamingService = null
        }
    }

    private val streamingCallback = object : StreamingService.Callback {
        override fun onStreamingStarted() {
            runOnUiThread { updateLiveUi() }
        }

        override fun onStreamingStopped(error: String?) {
            // Service-initiated stop (notification Stop action or an encoder/camera error):
            // the service has already released everything; just reflect it in the UI.
            runOnUiThread {
                unbindFromService()
                val status = if (error != null) {
                    getString(R.string.home_status_error, error)
                } else {
                    getString(R.string.home_status_disconnected)
                }
                teardownToHome(status)
            }
        }

        override fun onStreamHealth(health: StreamHealth) {
            runOnUiThread { updateLiveUi() }
        }

        override fun onCameraReady() {
            runOnUiThread { updateLiveUi() }
        }

        override fun onNoOtherCamera() {
            runOnUiThread {
                Toast.makeText(this@MainActivity, R.string.no_other_camera, Toast.LENGTH_SHORT).show()
            }
        }

        override fun onZoomChanged() {
            runOnUiThread { updateZoomRow() }
        }
    }

    private val phoneIdentity: PhoneIdentity by lazy {
        val id = prefs().getString("phone_id", null)
            ?: UUID.randomUUID().toString().also { prefs().edit().putString("phone_id", it).apply() }
        PhoneIdentity(id = id, name = Build.MODEL)
    }

    private val trustedLaptops: TrustedLaptops by lazy {
        val prefs = prefs()
        TrustedLaptops(
            load = { prefs.getString(TrustedLaptops.PREF_KEY, null) },
            save = { prefs.edit().putString(TrustedLaptops.PREF_KEY, it).apply() },
        )
    }

    private var pendingReconnect: TrustedLaptop? = null
    private var homeReconnectJob: Job? = null

    private fun prefs() = getSharedPreferences("phonecam", MODE_PRIVATE)

    private val requestCameraPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) {
                maybeRequestNotificationPermission()
                val laptop = pendingReconnect
                pendingReconnect = null
                if (laptop != null) {
                    startHomeReconnect(laptop)
                } else {
                    showScan()
                }
            } else {
                pendingReconnect = null
                showHome(getString(R.string.home_status_permission_needed))
            }
        }

    /** POST_NOTIFICATIONS on 33+: requested best-effort; streaming proceeds even if denied. */
    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* ignore */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        inflateChrome()

        if (StreamingService.isRunning) {
            // Reopened / recreated while a stream is live: rebind and show Live.
            screenState = ScreenState.LIVE
            showLive()
            bindToRunningService()
        } else {
            showHome(getString(R.string.home_status_not_connected))
        }

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                when (screenState) {
                    ScreenState.SCAN -> teardownToHome(getString(R.string.home_status_not_connected))
                    ScreenState.LIVE -> leaveSession(getString(R.string.home_status_disconnected))
                    ScreenState.HOME -> finish()
                }
            }
        })
    }

    /**
     * The activity keeps [configChanges] so a live stream is not torn down on rotate.
     * Re-inflate so [layout-land] is used; pairing and the bound service stay put.
     */
    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        val previous = screenState
        val status = binding.homeStatusText.text?.toString()
            ?: getString(R.string.home_status_not_connected)
        inflateChrome()
        when (previous) {
            ScreenState.HOME -> showHome(status)
            ScreenState.SCAN -> showScan()
            ScreenState.LIVE -> {
                showLive()
                attachPreviewIfLive()
            }
        }
    }

    private fun inflateChrome() {
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)
        applyEdgeToEdge()
        wireUp()
        binding.root.requestApplyInsets()
    }

    private fun applyEdgeToEdge() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        ViewCompat.setOnApplyWindowInsetsListener(binding.root) { _, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            binding.homeContainer.updatePadding(
                left = bars.left,
                top = bars.top,
                right = bars.right,
                bottom = bars.bottom,
            )
            binding.liveContainer.updatePadding(
                left = bars.left,
                top = bars.top,
                right = bars.right,
                bottom = bars.bottom,
            )
            val hintParams = binding.scanHint.layoutParams
            if (hintParams is android.widget.FrameLayout.LayoutParams) {
                hintParams.topMargin = bars.top + scanHintTopPad()
                binding.scanHint.layoutParams = hintParams
            }
            val cancelParams = binding.scanCancelButton.layoutParams
            if (cancelParams is android.widget.FrameLayout.LayoutParams) {
                cancelParams.bottomMargin = bars.bottom + scanCancelBottomPad()
                binding.scanCancelButton.layoutParams = cancelParams
            }
            applyPreviewAspect()
            insets
        }
    }

    private fun scanHintTopPad(): Int = (12 * resources.displayMetrics.density).toInt()

    private fun scanCancelBottomPad(): Int = (20 * resources.displayMetrics.density).toInt()

    private fun wireUp() {
        binding.scanConnectButton.setOnClickListener { onScanConnectClicked() }
        binding.exitButton.setOnClickListener { finish() }
        binding.scanCancelButton.setOnClickListener {
            teardownToHome(getString(R.string.home_status_not_connected))
        }
        binding.leaveButton.setOnClickListener {
            leaveSession(getString(R.string.home_status_disconnected))
        }
        binding.flipCameraButton.setOnClickListener {
            streamingService?.flipCamera()
            attachPreviewIfLive()
        }
        binding.zoomInButton.setOnClickListener { streamingService?.zoomIn() }
        binding.zoomOutButton.setOnClickListener { streamingService?.zoomOut() }
        binding.zoomResetButton.setOnClickListener { streamingService?.resetZoom() }
        binding.previewCard.addOnLayoutChangeListener { _, left, _, right, _, oldLeft, _, oldRight, _ ->
            if (right - left != oldRight - oldLeft) applyPreviewAspect()
        }
        setupQualitySelector()
    }

    /**
     * Load the persisted quality, reflect it in the toggle, and persist any user change
     * immediately. The choice must be made on Home BEFORE scanning (a QR pairs the instant it
     * is scanned), so this stays a Home-only control.
     */
    private fun setupQualitySelector() {
        selectedQuality = StreamQuality.fromKey(prefs().getString("stream_quality", null))
        binding.qualityToggleGroup.check(buttonIdFor(selectedQuality))
        binding.qualityToggleGroup.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            val quality = qualityFor(checkedId) ?: return@addOnButtonCheckedListener
            selectedQuality = quality
            prefs().edit().putString("stream_quality", quality.key).apply()
        }
    }

    private fun buttonIdFor(quality: StreamQuality): Int = when (quality) {
        StreamQuality.HIGH -> R.id.qualityHigh
        StreamQuality.MEDIUM -> R.id.qualityMedium
        StreamQuality.LOW -> R.id.qualityLow
    }

    private fun qualityFor(buttonId: Int): StreamQuality? = when (buttonId) {
        R.id.qualityHigh -> StreamQuality.HIGH
        R.id.qualityMedium -> StreamQuality.MEDIUM
        R.id.qualityLow -> StreamQuality.LOW
        else -> null
    }

    // ------------------------------------------------------------------ Screen switching

    private fun showHome(status: String) {
        screenState = ScreenState.HOME
        binding.homeStatusText.text = status
        paintHomeStatusDot(status)
        binding.homeContainer.visibility = View.VISIBLE
        binding.scanContainer.visibility = View.GONE
        binding.liveContainer.visibility = View.GONE
        renderReconnectList()
    }

    private fun renderReconnectList() {
        val list = binding.reconnectList
        list.removeAllViews()
        val laptops = trustedLaptops.list()
        if (laptops.isEmpty()) {
            list.visibility = View.GONE
            return
        }
        list.visibility = View.VISIBLE
        for (laptop in laptops) {
            val btn = layoutInflater.inflate(R.layout.item_reconnect, list, false) as MaterialButton
            val label = laptop.name.ifBlank { laptop.laptopId }
            btn.text = getString(R.string.btn_reconnect_to, label)
            btn.setOnClickListener { onReconnectClicked(laptop) }
            btn.setOnLongClickListener {
                confirmForget(laptop)
                true
            }
            list.addView(btn)
        }
    }

    private fun confirmForget(laptop: TrustedLaptop) {
        MaterialAlertDialogBuilder(this)
            .setMessage(R.string.forget_laptop_copy)
            .setPositiveButton(R.string.btn_forget) { _, _ ->
                trustedLaptops.forget(laptop.laptopId)
                renderReconnectList()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun onReconnectClicked(laptop: TrustedLaptop) {
        if (StreamingService.isRunning) {
            showHome(getString(R.string.home_status_stopping))
            watchServiceStop()
            return
        }
        if (!hasCameraPermission()) {
            pendingReconnect = laptop
            requestCameraPermission.launch(Manifest.permission.CAMERA)
            return
        }
        maybeRequestNotificationPermission()
        startHomeReconnect(laptop)
    }

    private fun startHomeReconnect(laptop: TrustedLaptop) {
        if (homeReconnectJob != null) return
        val committed = selectedQuality.toProfile()
        committedProfile = committed
        showHome(getString(R.string.home_status_reconnecting, laptop.name.ifBlank { laptop.laptopId }))
        homeReconnectJob = lifecycleScope.launch {
            try {
                val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
                rtpIdentity = rtp
                val result = withContext(Dispatchers.IO) {
                    HomeReconnect(HttpControlClient()).connect(
                        laptop = laptop,
                        phone = phoneIdentity,
                        rtp = rtp,
                        video = committed,
                        camera = persistedCameraFacing(),
                    )
                }
                when (result) {
                    is HomeReconnectResult.Ready -> {
                        persistTrustedLaptop(result.payload, result.pairingSecret)
                        payload = result.payload
                        committedProfile = result.profile
                        showLive()
                        onPaired(
                            PairingState.Paired(
                                payload = result.payload,
                                resumeToken = result.resumeToken,
                                pairingSecret = result.pairingSecret,
                            )
                        )
                    }
                    is HomeReconnectResult.Failure -> {
                        rtp.close()
                        rtpIdentity = null
                        showHome(result.message)
                    }
                }
            } catch (e: Exception) {
                rtpIdentity?.close()
                rtpIdentity = null
                showHome(
                    getString(
                        R.string.home_status_cant_reach,
                        laptop.name.ifBlank { laptop.laptopId },
                    )
                )
            } finally {
                homeReconnectJob = null
            }
        }
    }

    private fun paintHomeStatusDot(status: String) {
        val colorRes = when {
            status.startsWith("Error") -> R.color.pc_tally
            status == getString(R.string.home_status_disconnected) -> R.color.pc_tungsten
            status == getString(R.string.home_status_stopping) -> R.color.pc_tungsten
            status == getString(R.string.home_status_permission_needed) -> R.color.pc_tungsten
            else -> R.color.pc_tungsten_dim
        }
        val color = ResourcesCompat.getColor(resources, colorRes, theme)
        val base: Drawable = binding.homeStatusDot.background?.mutate()
            ?: ResourcesCompat.getDrawable(resources, R.drawable.bg_status_dot, theme)
            ?: return
        DrawableCompat.setTint(base, color)
        binding.homeStatusDot.background = base
    }

    private fun onScanConnectClicked() {
        if (StreamingService.isRunning) {
            showHome(getString(R.string.home_status_stopping))
            watchServiceStop()
            return
        }
        if (hasCameraPermission()) {
            maybeRequestNotificationPermission()
            showScan()
        } else {
            requestCameraPermission.launch(Manifest.permission.CAMERA)
        }
    }

    private fun maybeRequestNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val granted = ContextCompat.checkSelfPermission(
            this, Manifest.permission.POST_NOTIFICATIONS
        ) == PackageManager.PERMISSION_GRANTED
        if (!granted) requestNotificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
    }

    private fun showScan() {
        screenState = ScreenState.SCAN
        leaveRequested = false
        handledPayload = false
        binding.homeContainer.visibility = View.GONE
        binding.liveContainer.visibility = View.GONE
        binding.scanContainer.visibility = View.VISIBLE
        bindScanCamera()
    }

    /** Enter the Live screen; the connecting/live state is rendered by [updateLiveUi]. */
    private fun showLive() {
        screenState = ScreenState.LIVE
        binding.homeContainer.visibility = View.GONE
        binding.scanContainer.visibility = View.GONE
        binding.liveContainer.visibility = View.VISIBLE
        updateLiveUi()
    }

    /**
     * Full teardown to Home: cancel pairing, unbind from the service, release any scan camera
     * and pairing-phase resources. Does NOT stop the service — callers that must stop a live
     * stream go through [leaveSession].
     */
    private fun teardownToHome(status: String) {
        pairingJob?.cancel()
        pairingJob = null
        homeReconnectJob?.cancel()
        homeReconnectJob = null
        handledPayload = false
        unbindFromService()
        keepScreenOn(false)
        // Close a not-yet-handed-off RTP socket (pairing failed before Paired).
        rtpIdentity?.close()
        rtpIdentity = null
        unbindScanCamera()
        payload = null
        committedProfile = null
        showHome(status)
    }

    /** User-initiated stop of a live session (Leave / Cancel / back on Live), then Home. */
    private fun leaveSession(status: String) {
        val current = payload ?: StreamingSession.payload
        if (current != null) {
            lifecycleScope.launch(Dispatchers.IO) {
                try {
                    HttpControlClient().leave(current)
                } catch (_: Exception) {
                }
            }
        }
        stopStreamingFromUi()
        val stopping = StreamingService.isRunning || bound
        teardownToHome(
            if (stopping) getString(R.string.home_status_stopping) else status,
        )
        if (stopping) watchServiceStop()
    }

    private fun watchServiceStop() {
        stopWatchJob?.cancel()
        stopWatchJob = lifecycleScope.launch {
            while (StreamingService.isRunning) delay(40)
            if (screenState == ScreenState.HOME) {
                showHome(getString(R.string.home_status_not_connected))
            }
        }
    }

    /**
     * Stop the streaming service even when the binder has not arrived yet.
     * [onPaired] calls [ContextCompat.startForegroundService] then [bindService]; Cancel
     * in that window used to unbind and return Home while the service kept streaming.
     */
    private fun stopStreamingFromUi() {
        leaveRequested = true
        val svc = streamingService
        if (svc != null) {
            svc.setCallback(null)
            svc.stopFromActivity()
            return
        }
        StreamingSession.clearAndClose()
        if (StreamingService.isRunning || bound) {
            startService(StreamingService.stopIntent(this))
        }
    }

    private fun hasCameraPermission(): Boolean =
        ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) ==
            PackageManager.PERMISSION_GRANTED

    // ------------------------------------------------------------------ Scan camera

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

    /** Bind the QR-scanning pipeline (back camera) to the fullscreen scan preview. */
    private fun bindScanCamera() {
        withProvider { provider ->
            val preview = Preview.Builder().build().also {
                it.surfaceProvider = binding.scanPreview.surfaceProvider
            }
            scanPreview = preview
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
            // Scanning and streaming are mutually exclusive (Scan is only reachable from Home,
            // which means the service is stopped), so clearing all use cases here is safe.
            provider.unbindAll()
            try {
                provider.bindToLifecycle(this, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis)
            } catch (e: Exception) {
                failToHome(e.localizedMessage ?: e.toString())
            }
        }
    }

    /** Unbind only the scan use cases (never [ProcessCameraProvider.unbindAll], which would
     * also drop the service's live streaming use cases in this shared singleton provider). */
    private fun unbindScanCamera() {
        imageAnalysis?.clearAnalyzer()
        val provider = cameraProvider
        if (provider != null) {
            val toUnbind = listOfNotNull(scanPreview, imageAnalysis).toTypedArray()
            if (toUnbind.isNotEmpty()) provider.unbind(*toUnbind)
        }
        scanPreview = null
        imageAnalysis = null
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
     * A valid QR was found: stop scanning, release the scan camera, open the Live screen, and
     * IMMEDIATELY begin pairing — no button in between.
     */
    private fun onPayloadDetected(detected: PairingPayload) {
        unbindScanCamera()
        pairingJob?.cancel()
        pairingJob = null
        payload = detected
        showLive()
        startPairing()
    }

    // ------------------------------------------------------------------ Pairing

    /**
     * Kick off the handshake straight away. The `/pair` dims are the payload's base landscape
     * canvas verbatim — never swapped — and on [PairingState.Paired] streaming starts in the
     * service automatically. Rotation is handled per frame, so nothing about orientation is
     * committed here.
     */
    private fun startPairing() {
        val current = payload ?: return
        // Guard rapid QR re-detections: don't launch a second handshake while one is running.
        if (pairingJob != null) return

        // The chosen quality — NOT the QR payload's `video` dims — is the canonical session
        // resolution: it sizes the encoder, the composition canvas, the streaming ImageAnalysis
        // target (in the service), and the dims announced to the laptop at `/pair`. The Linux
        // receiver adapts to whatever dims the phone reports, so no receiver change is needed.
        val committed = selectedQuality.toProfile()
        committedProfile = committed
        updateLiveUi()

        leaveRequested = false
        pairingJob = lifecycleScope.launch {
            val rtp = withContext(Dispatchers.IO) { RtpIdentity.create() }
            rtpIdentity = rtp
            val controller = PairingController(
                HttpControlClient(),
                phoneIdentity,
                rtp,
                committed,
                camera = persistedCameraFacing(),
            )
            launch {
                repeatOnLifecycle(Lifecycle.State.STARTED) {
                    controller.state.collect { state ->
                        when (state) {
                            is PairingState.Paired -> onPaired(state)
                            is PairingState.Failed -> failToHome(state.message)
                            else -> updateLiveUi()
                        }
                    }
                }
            }
            withContext(Dispatchers.IO) { controller.run(current) }
        }
    }

    // ------------------------------------------------------------------ Streaming handoff

    /**
     * Pairing approved: hand the session to the [StreamingService] and start + bind it. The
     * service must be started while the app is visible (Android's while-in-use camera rule) —
     * pairing happens on the visible Live screen, so that holds. Idempotent against rapid
     * re-emission of [PairingState.Paired].
     */
    private fun onPaired(paired: PairingState.Paired) {
        val current = payload ?: return
        val profile = committedProfile ?: return
        val rtp = rtpIdentity ?: return
        if (leaveRequested) {
            rtp.close()
            rtpIdentity = null
            return
        }
        if (bound || StreamingService.isRunning) {
            if (!waitingToStartService) {
                waitingToStartService = true
                lifecycleScope.launch {
                    try {
                        while ((bound || StreamingService.isRunning) && !leaveRequested) {
                            delay(40)
                        }
                        if (!leaveRequested) onPaired(paired)
                    } finally {
                        waitingToStartService = false
                    }
                }
            }
            return
        }

        persistTrustedLaptop(current, paired.pairingSecret)

        StreamingSession.payload = current
        StreamingSession.profile = profile
        StreamingSession.rtpIdentity = rtp
        StreamingSession.resumeToken = paired.resumeToken
        StreamingSession.pairingSecret = paired.pairingSecret
        StreamingSession.phone = phoneIdentity
        rtpIdentity = null // ownership (and the open socket) transfers to the service

        keepScreenOn(true)
        sizePreviewCard(profile.width, profile.height)

        val intent = Intent(this, StreamingService::class.java)
        ContextCompat.startForegroundService(this, intent)
        bindService(intent, serviceConnection, Context.BIND_AUTO_CREATE)
        bound = true
        updateLiveUi()
    }

    private fun bindToRunningService() {
        if (bound) return
        val intent = Intent(this, StreamingService::class.java)
        bindService(intent, serviceConnection, Context.BIND_AUTO_CREATE)
        bound = true
    }

    private fun unbindFromService() {
        if (!bound) return
        streamingService?.setCallback(null)
        streamingService?.detachPreview()
        try {
            unbindService(serviceConnection)
        } catch (_: IllegalArgumentException) {
            // Not registered (already unbound); ignore.
        }
        bound = false
        streamingService = null
    }

    private fun isStreaming(): Boolean = streamingService?.isStreaming() == true

    // ------------------------------------------------------------------ Keep-screen-on

    /** Activity-side only: meaningful while the Live screen is visible during streaming. */
    private fun keepScreenOn(on: Boolean) {
        if (on) {
            window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        } else {
            window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        }
    }

    // ------------------------------------------------------------------ Live UI

    /** Render the Live screen: connecting until the service is streaming, then REC + laptop name. */
    private fun updateLiveUi() {
        if (screenState != ScreenState.LIVE) return
        val streaming = isStreaming()
        val profile = streamingService?.profile() ?: committedProfile ?: selectedQuality.toProfile()
        binding.leaveButton.visibility = View.VISIBLE
        binding.liveQualityCaption.visibility = View.VISIBLE
        binding.liveQualityCaption.text = getString(
            R.string.live_quality_caption,
            profile.height,
            profile.fps,
        )
        sizePreviewCard(profile.width, profile.height)
        val health = streamingService?.streamHealth()
        val reconnecting = streaming &&
            (health is StreamHealth.Reconnecting || health is StreamHealth.Failed)
        val showFlip = streaming && streamingService?.canFlipCamera() == true
        binding.flipCameraButton.visibility = if (showFlip) View.VISIBLE else View.GONE
        if (streaming && reconnecting) {
            val name = streamingService?.laptopName() ?: payload?.name.orEmpty()
            binding.liveStatus.text = getString(R.string.live_reconnecting, name)
            binding.liveRecBadge.visibility = View.GONE
            binding.liveConnectingProgress.visibility = View.VISIBLE
            binding.leaveButton.visibility = View.VISIBLE
            binding.leaveButton.setText(R.string.btn_leave)
            // Viewfinder is always visible while streaming; (re)hook its surface.
            attachPreviewIfLive()
            updateBatteryHint()
        } else if (streaming) {
            val name = streamingService?.laptopName() ?: payload?.name.orEmpty()
            binding.liveStatus.text = getString(R.string.live_status, name)
            binding.liveRecBadge.visibility = View.VISIBLE
            binding.liveConnectingProgress.visibility = View.GONE
            binding.leaveButton.setText(R.string.btn_leave)
            // Viewfinder is always visible while streaming; (re)hook its surface.
            attachPreviewIfLive()
            updateBatteryHint()
        } else {
            val name = payload?.name.orEmpty()
            binding.liveStatus.text = getString(R.string.live_connecting, name)
            binding.liveRecBadge.visibility = View.GONE
            binding.liveConnectingProgress.visibility = View.VISIBLE
            binding.leaveButton.setText(R.string.btn_cancel)
            binding.liveBatteryHint.visibility = View.GONE
            binding.previewCard.visibility = View.VISIBLE
        }
        updateZoomRow()
    }

    /**
     * Reflect the streaming camera's zoom in the LIVE zoom row: readout, button bounds, and
     * the whole row's visibility (hidden while connecting and for lenses without a zoom
     * range, e.g. many front cameras). State arrives from [StreamingService.Callback.onZoomChanged].
     */
    private fun updateZoomRow() {
        if (screenState != ScreenState.LIVE) return
        val zoom = if (isStreaming()) streamingService?.currentZoom() else null
        val show = zoom != null && ZoomStepper.shouldShow(zoom.minRatio, zoom.maxRatio)
        binding.zoomRow.visibility = if (show) View.VISIBLE else View.GONE
        if (zoom == null || !show) return
        binding.zoomReadout.text = ZoomStepper.format(zoom.ratio)
        val zoomInEnabled = ZoomStepper.canZoomIn(zoom.ratio, zoom.maxRatio)
        val zoomOutEnabled = ZoomStepper.canZoomOut(zoom.ratio, zoom.minRatio)
        val resetEnabled = ZoomStepper.canReset(zoom.ratio, zoom.minRatio, zoom.maxRatio)
        binding.zoomInButton.isEnabled = zoomInEnabled
        binding.zoomInButton.alpha = if (zoomInEnabled) 1f else DISABLED_BUTTON_ALPHA
        binding.zoomOutButton.isEnabled = zoomOutEnabled
        binding.zoomOutButton.alpha = if (zoomOutEnabled) 1f else DISABLED_BUTTON_ALPHA
        binding.zoomResetButton.isEnabled = resetEnabled
        binding.zoomResetButton.alpha = if (resetEnabled) 1f else DISABLED_BUTTON_ALPHA
    }

    /** One-line hint when battery optimizations are NOT exempted for this app. */
    private fun updateBatteryHint() {
        val power = getSystemService(Context.POWER_SERVICE) as PowerManager
        val exempt = power.isIgnoringBatteryOptimizations(packageName)
        binding.liveBatteryHint.visibility = if (exempt) View.GONE else View.VISIBLE
    }

    /** Attach the live preview to the service's Preview use case while visible + streaming. */
    private fun attachPreviewIfLive() {
        if (screenState == ScreenState.LIVE && isStreaming()) {
            streamingService?.attachPreview(binding.livePreview.surfaceProvider)
        }
    }

    /** Remember the committed canvas ratio and apply it from the laid-out card width. */
    private fun sizePreviewCard(streamWidth: Int, streamHeight: Int) {
        if (streamWidth <= 0 || streamHeight <= 0) return
        previewAspectWidth = streamWidth
        previewAspectHeight = streamHeight
        applyPreviewAspect()
        if (binding.liveStage.width <= 0) {
            binding.liveStage.post { applyPreviewAspect() }
        }
    }

    /**
     * Fit the 16:9 canvas into [liveStage]. Portrait uses the stage width; landscape
     * also caps height so a full-width preview cannot overflow the short side.
     */
    private fun applyPreviewAspect() {
        val stage = binding.liveStage
        val availW = stage.width
        if (availW <= 0 || previewAspectWidth <= 0) return
        val landscape =
            resources.configuration.orientation == Configuration.ORIENTATION_LANDSCAPE
        val maxH = if (landscape && stage.height > 0) stage.height else Int.MAX_VALUE
        var width = availW
        var height = (width.toLong() * previewAspectHeight / previewAspectWidth)
            .toInt()
            .coerceAtLeast(1)
        if (height > maxH) {
            height = maxH
            width = (height.toLong() * previewAspectWidth / previewAspectHeight)
                .toInt()
                .coerceAtLeast(1)
        }
        val previewParams = binding.livePreview.layoutParams
        val previewWidth = if (landscape) width else ViewGroup.LayoutParams.MATCH_PARENT
        if (previewParams.width != previewWidth || previewParams.height != height) {
            previewParams.width = previewWidth
            previewParams.height = height
            binding.livePreview.layoutParams = previewParams
        }
        // Overlay is match_parent of a wrap_content parent; pin it to the preview
        // so the corners sit on the image in landscape, not a stretched 16:9 vector.
        val overlay = binding.viewfinderOverlay.root
        val overlayParams = overlay.layoutParams
        if (overlayParams.width != previewWidth || overlayParams.height != height) {
            overlayParams.width = previewWidth
            overlayParams.height = height
            overlay.layoutParams = overlayParams
        }

        val cardParams = binding.previewCard.layoutParams
        if (landscape) {
            cardParams.width = ViewGroup.LayoutParams.WRAP_CONTENT
            cardParams.height = ViewGroup.LayoutParams.WRAP_CONTENT
            if (cardParams is FrameLayout.LayoutParams) {
                cardParams.gravity = Gravity.CENTER
            }
        } else {
            cardParams.width = ViewGroup.LayoutParams.MATCH_PARENT
            cardParams.height = ViewGroup.LayoutParams.WRAP_CONTENT
        }
        binding.previewCard.layoutParams = cardParams
    }

    /** Any pairing failure (pre-streaming): surface the reason on Home's status card. */
    private fun failToHome(message: String) {
        teardownToHome(getString(R.string.home_status_error, message))
    }

    private fun persistedCameraFacing(): String =
        CameraFacing.fromPref(prefs().getString(CameraFacing.PREF_KEY, null))

    /** Persist pairing_secret before startForegroundService when the QR had a laptop_id. */
    private fun persistTrustedLaptop(payload: PairingPayload, secret: String?) {
        if (payload.laptopId.isBlank() || secret.isNullOrBlank()) return
        trustedLaptops.upsert(
            TrustedLaptop(
                laptopId = payload.laptopId,
                name = payload.name,
                control = payload.control,
                rtp = payload.rtp,
                secret = secret,
                lastSeen = Instant.now().toString(),
            )
        )
    }

    // ------------------------------------------------------------------ Activity lifecycle

    override fun onStart() {
        super.onStart()
        attachPreviewIfLive()
    }

    override fun onStop() {
        super.onStop()
        // Detach the preview when not visible; streaming (service-owned) is unaffected.
        streamingService?.detachPreview()
    }

    override fun onDestroy() {
        super.onDestroy()
        pairingJob?.cancel()
        homeReconnectJob?.cancel()
        // Unbind WITHOUT stopping the service — streaming must continue when the activity is
        // destroyed (task removed, recreate). Do NOT unbindAll the shared camera provider.
        unbindFromService()
        unbindScanCamera()
        analysisExecutor.shutdown()
        barcodeScanner.close()
        // Close a not-yet-handed-off RTP socket, if any (pairing was still in flight).
        rtpIdentity?.close()
    }
    private companion object {
        /** Zoom buttons dim at their bounds; the outlined style's static tints don't fade. */
        const val DISABLED_BUTTON_ALPHA = 0.38f
    }
}
