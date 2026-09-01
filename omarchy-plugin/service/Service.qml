import QtQuick
import Quickshell
import Quickshell.Io
import "../Model.js" as Model

Item {
  id: root

  property var shell: null
  property var manifest: null
  property var settings: ({})

  readonly property string sourceDir: manifest && manifest.__sourceDir ? String(manifest.__sourceDir) : ""
  readonly property string qrHelper: sourceDir === "" ? "" : sourceDir + "/bin/phonecam-qr"
  readonly property string homeDir: Quickshell.env("HOME")
  readonly property string runtimeDir: Quickshell.env("XDG_RUNTIME_DIR")
  readonly property string sessionPath: runtimeDir ? runtimeDir + "/phonecam/session.json" : ""

  readonly property int controlPort: Model.normalizePort(setting("controlPort", 47470), 47470)
  readonly property int rtpPort: Model.normalizePort(setting("rtpPort", 47471), 47471)
  readonly property int activeControlPort: sessionRecord && sessionRecord.control_port
    ? sessionRecord.control_port
    : controlPort

  property string phase: "stopped"
  property string lastError: ""
  property string actionStatus: ""
  property var status: ({})
  property var pairing: null
  property var doctorChecks: []
  property bool doctorBlocking: false
  property var qrRows: []
  property int qrSize: 0
  property var trusted: []
  property bool pairingExpired: false
  property string resolvedBin: ""
  property var sessionRecord: null

  readonly property bool startOwned: startProcess.running
  readonly property bool busy: startProcess.running === false && (stopProcess.running || doctorProcess.running || discoverProcess.running)
  readonly property bool canStart: resolvedBin !== "" && !startProcess.running && phase !== "starting" && phase !== "waiting" && phase !== "live" && phase !== "silent"
  readonly property bool canStop: startProcess.running || phase === "waiting" || phase === "live" || phase === "silent" || phase === "starting" || !!sessionRecord
  readonly property bool canRestart: resolvedBin !== "" && (canStop || phase === "error")

  property string _discoverOutput: ""
  property string _doctorOutput: ""
  property string _doctorError: ""
  property string _stopError: ""
  property string _startStderr: ""
  property string _probeKind: ""
  property string _probeOutput: ""
  property string _probeError: ""
  property var _probeQueue: []
  property bool _intentionalStop: false
  property bool _restartAfterStop: false
  property bool _startAfterDoctor: false
  property bool _startAfterHealthz: false
  property bool _crash: false
  property var _pairingFull: null
  property string _lastQrJson: ""
  property string _qrPayload: ""
  property bool _healthz: false
  property bool _adopted: false
  property double nowMs: Date.now()

  function setting(name, fallback) {
    var value = settings ? settings[name] : undefined
    return value === undefined || value === null ? fallback : value
  }

  function applyPhase() {
    if (resolvedBin === "") {
      phase = "missing-binary"
      return
    }
    if (_crash && !_healthz && !startProcess.running) {
      phase = "error"
      pairingExpired = false
      return
    }
    phase = Model.derivePhase({
      binary: true,
      startRunning: startProcess.running,
      healthz: _healthz,
      approved: !!(status && status.approved),
      last_rtp_ms: status && status.last_rtp_ms
    })
    pairingExpired = Model.pairingExpired(_pairingFull, nowMs)
  }

  function clearLiveState() {
    status = ({})
    pairing = null
    _pairingFull = null
    qrRows = []
    qrSize = 0
    _lastQrJson = ""
    _healthz = false
    pairingExpired = false
    trusted = []
  }

  function start() {
    if (resolvedBin === "") {
      lastError = "PhoneCam CLI not found"
      actionStatus = ""
      return
    }
    if (phase === "waiting" || phase === "live" || phase === "silent" || phase === "starting") {
      actionStatus = "Already running"
      actionStatusTimer.restart()
      return
    }
    if (sessionRecord && !_crash) {
      enqueueProbe("healthz")
    }
    lastError = ""
    _crash = false
    _intentionalStop = false
    _startAfterDoctor = true
    actionStatus = "Checking doctor"
    actionStatusTimer.restart()
    refreshDoctor()
  }

  function actuallyStart() {
    if (startProcess.running || resolvedBin === "") return
    if (sessionRecord) {
      _startAfterHealthz = true
      enqueueProbe("healthz")
      return
    }
    launchStart()
  }

  function launchStart() {
    _intentionalStop = false
    _crash = false
    _adopted = false
    _startStderr = ""
    lastError = ""
    phase = "starting"
    startProcess.command = Model.startArgv(resolvedBin, controlPort, rtpPort)
    startProcess.running = true
  }

  function stop() {
    if (resolvedBin === "") return
    _intentionalStop = true
    _startAfterDoctor = false
    _crash = false
    if (stopProcess.running) return
    stopProcess.command = [resolvedBin, "stop"]
    stopProcess.running = true
  }

  function restart() {
    if (resolvedBin === "") return
    _restartAfterStop = true
    if (canStop) stop()
    else start()
  }

  function refreshDoctor() {
    if (resolvedBin === "" || doctorProcess.running) return
    _doctorOutput = ""
    _doctorError = ""
    doctorProcess.command = [resolvedBin, "doctor"]
    doctorProcess.running = true
  }

  function refreshTrust() {
    if (!_healthz && phase !== "waiting" && phase !== "live" && phase !== "silent" && phase !== "starting") return
    enqueueProbe("trust")
  }

  function revokeTrust(id) {
    var safe = Model.safeTrustId(id)
    if (safe === "") return
    enqueueProbe("delete", safe)
  }

  function enqueueProbe(kind, extra) {
    if (kind !== "delete") {
      for (var i = 0; i < _probeQueue.length; i++) {
        if (_probeQueue[i].kind === kind) return
      }
      if (_probeKind === kind) return
    }
    var queue = _probeQueue.slice()
    queue.push({ kind: kind, extra: extra || "" })
    _probeQueue = queue
    runProbe()
  }

  function runProbe() {
    if (probeProcess.running || _probeKind !== "") return
    if (_probeQueue.length === 0) return
    var queue = _probeQueue.slice()
    var job = queue.shift()
    _probeQueue = queue
    _probeKind = job.kind
    _probeOutput = ""
    _probeError = ""
    var url = ""
    var method = "GET"
    if (job.kind === "healthz") url = Model.loopbackUrl(activeControlPort, "/healthz")
    else if (job.kind === "status") url = Model.loopbackUrl(activeControlPort, "/status")
    else if (job.kind === "pairing") url = Model.loopbackUrl(activeControlPort, "/pairing")
    else if (job.kind === "trust") url = Model.loopbackUrl(activeControlPort, "/trust")
    else if (job.kind === "delete") {
      var id = Model.safeTrustId(job.extra)
      if (id === "") {
        _probeKind = ""
        runProbe()
        return
      }
      url = Model.loopbackUrl(activeControlPort, "/trust/" + id)
      method = "DELETE"
    } else {
      _probeKind = ""
      runProbe()
      return
    }
    probeProcess.command = Model.curlArgs(method, url)
    probeProcess.running = true
  }

  function finishProbe(exitCode) {
    var kind = _probeKind
    var raw = String(_probeOutput || probeStdout.text || "")
    var err = Model.concise(_probeError || probeStderr.text || "")
    _probeKind = ""
    if (kind === "healthz") {
      var health = Model.parseJsonObject(raw)
      _healthz = exitCode === 0 && !!(health && health.ok === true)
      if (_startAfterHealthz) {
        _startAfterHealthz = false
        if (_healthz) {
          _adopted = true
          actionStatus = "Adopting running session"
          actionStatusTimer.restart()
          enqueueProbe("status")
          applyPhase()
        } else {
          launchStart()
        }
      } else if (_healthz) {
        if (sessionRecord) _adopted = true
        enqueueProbe("status")
        applyPhase()
      } else if (!startProcess.running && _adopted) {
        _adopted = false
        clearLiveState()
        sessionRecord = null
        applyPhase()
      } else {
        applyPhase()
      }
    } else if (kind === "status") {
      if (exitCode === 0) {
        var parsed = Model.parseStatus(raw)
        if (parsed) {
          status = parsed
          if (parsed.approved) {
            pairing = null
            _pairingFull = null
            qrRows = []
            qrSize = 0
            _lastQrJson = ""
          } else {
            enqueueProbe("pairing")
          }
        }
      }
      applyPhase()
    } else if (kind === "pairing") {
      if (exitCode === 0) {
        var payload = Model.parsePairing(raw)
        if (payload) {
          _pairingFull = payload
          pairing = Model.publicPairing(payload)
          pairingExpired = Model.pairingExpired(payload, nowMs)
          if (!pairingExpired) generateQr(payload)
          else {
            qrRows = []
            qrSize = 0
            _lastQrJson = ""
          }
        }
      }
      applyPhase()
    } else if (kind === "trust") {
      if (exitCode === 0) trusted = Model.parseTrust(raw)
    } else if (kind === "delete") {
      if (exitCode === 0) {
        actionStatus = "Trust revoked"
        actionStatusTimer.restart()
        enqueueProbe("trust")
      } else if (err !== "") {
        lastError = err
        actionStatusTimer.restart()
      }
    }
    runProbe()
  }

  function generateQr(payload) {
    if (qrHelper === "" || qrProcess.running) return
    var json = Model.compactJson(payload)
    if (json === "" || json === _lastQrJson) return
    _lastQrJson = json
    _qrPayload = json
    qrProcess.command = [qrHelper]
    qrProcess.running = true
  }

  function finishDoctor(exitCode) {
    var checks = Model.parseDoctor(_doctorOutput || doctorStdout.text || "")
    doctorChecks = checks
    doctorBlocking = Model.doctorBlocking(checks)
    if (_startAfterDoctor) {
      _startAfterDoctor = false
      if (doctorBlocking) {
        lastError = "Doctor blocked start"
        actionStatus = ""
        applyPhase()
        return
      }
      actuallyStart()
    }
  }

  function finishStop(exitCode) {
    clearLiveState()
    _adopted = false
    sessionRecord = null
    if (!_intentionalStop && exitCode !== 0) {
      lastError = Model.concise(_stopError || "phonecam stop failed")
    } else {
      lastError = ""
    }
    phase = resolvedBin === "" ? "missing-binary" : "stopped"
    if (_restartAfterStop) {
      _restartAfterStop = false
      _intentionalStop = false
      Qt.callLater(function() { root.start() })
    }
  }

  function finishDiscover() {
    var path = String(_discoverOutput || discoverStdout.text || "").trim().split("\n")[0] || ""
    if (path !== "") {
      resolvedBin = path
      if (phase === "missing-binary") phase = "stopped"
      sessionFile.reload()
      return
    }
    resolvedBin = ""
    phase = "missing-binary"
  }

  function beginDiscover() {
    if (discoverProcess.running) return
    _discoverOutput = ""
    var binSetting = String(setting("binaryPath", "")).trim()
    var script = [
      "set -e",
      "if [ -n \"$PHONECAM_BIN\" ] && [ -x \"$PHONECAM_BIN\" ]; then printf '%s\\n' \"$PHONECAM_BIN\"; exit 0; fi",
      "if command -v phonecam >/dev/null 2>&1; then command -v phonecam; exit 0; fi",
      "if [ -x \"$HOME/.local/bin/phonecam\" ]; then printf '%s\\n' \"$HOME/.local/bin/phonecam\"; exit 0; fi",
      "gopath=$(go env GOPATH 2>/dev/null || true)",
      "if [ -n \"$gopath\" ] && [ -x \"$gopath/bin/phonecam\" ]; then printf '%s\\n' \"$gopath/bin/phonecam\"; exit 0; fi",
      "exit 1"
    ].join("\n")
    discoverProcess.command = ["bash", "-c", script]
    var env = ({})
    env.HOME = homeDir
    env.PATH = Quickshell.env("PATH")
    env.PHONECAM_BIN = binSetting
    discoverProcess.environment = env
    discoverProcess.running = true
  }

  Component.onCompleted: beginDiscover()

  onSettingsChanged: beginDiscover()

  Component.onDestruction: {
    _intentionalStop = true
    pollTimer.stop()
    if (probeProcess.running) probeProcess.running = false
    if (qrProcess.running) qrProcess.running = false
    if (doctorProcess.running) doctorProcess.running = false
    // Leave phonecam start running; pdeathsig TERM handles shell exit.
  }

  Timer {
    id: actionStatusTimer
    interval: 3000
    repeat: false
    onTriggered: root.actionStatus = ""
  }

  Timer {
    id: clockTimer
    interval: 1000
    repeat: true
    running: root.phase === "waiting"
    onTriggered: {
      root.nowMs = Date.now()
      root.pairingExpired = Model.pairingExpired(root._pairingFull, root.nowMs)
      if (root.pairingExpired) {
        root.qrRows = []
        root.qrSize = 0
      }
    }
  }

  Timer {
    id: pollTimer
    interval: 2000
    repeat: true
    running: root.startOwned || !!root.sessionRecord || root.phase === "starting" || root.phase === "waiting" || root.phase === "live" || root.phase === "silent"
    triggeredOnStart: true
    onTriggered: root.enqueueProbe("healthz")
  }

  FileView {
    id: sessionFile
    path: root.sessionPath
    watchChanges: true
    printErrors: false
    onLoaded: {
      root.sessionRecord = Model.parseSessionRecord(text())
      if (root.sessionRecord && root.resolvedBin !== "") root.enqueueProbe("healthz")
    }
    onLoadFailed: {
      if (!root.startOwned) root.sessionRecord = null
    }
    onFileChanged: reload()
  }

  Process {
    id: discoverProcess
    command: []
    running: false
    stdout: StdioCollector {
      id: discoverStdout
      waitForEnd: true
      onStreamFinished: root._discoverOutput = text
    }
    stderr: StdioCollector { waitForEnd: true }
    onExited: root.finishDiscover()
  }

  Process {
    id: startProcess
    command: []
    running: false
    stdout: SplitParser {
      onRead: function(line) { }
    }
    stderr: SplitParser {
      onRead: function(line) {
        var value = Model.concise(line)
        if (value !== "") root._startStderr = value
      }
    }
    onStarted: {
      root.phase = "starting"
      root._healthz = false
    }
    onExited: function(exitCode) {
      if (root._intentionalStop) {
        root.clearLiveState()
        if (!stopProcess.running) {
          root.phase = root.resolvedBin === "" ? "missing-binary" : "stopped"
        }
        return
      }
      root._crash = true
      root.clearLiveState()
      root.lastError = root._startStderr || "PhoneCam start exited"
      root._startStderr = ""
      root.applyPhase()
    }
  }

  Process {
    id: stopProcess
    command: []
    running: false
    stdout: StdioCollector { waitForEnd: true }
    stderr: StdioCollector {
      id: stopStderr
      waitForEnd: true
      onStreamFinished: root._stopError = text
    }
    onExited: function(exitCode) { root.finishStop(exitCode) }
  }

  Process {
    id: doctorProcess
    command: []
    running: false
    stdout: StdioCollector {
      id: doctorStdout
      waitForEnd: true
      onStreamFinished: root._doctorOutput = text
    }
    stderr: StdioCollector {
      waitForEnd: true
      onStreamFinished: root._doctorError = text
    }
    onExited: function(exitCode) { root.finishDoctor(exitCode) }
  }

  Process {
    id: probeProcess
    command: []
    running: false
    stdout: StdioCollector {
      id: probeStdout
      waitForEnd: true
      onStreamFinished: root._probeOutput = text
    }
    stderr: StdioCollector {
      id: probeStderr
      waitForEnd: true
      onStreamFinished: root._probeError = text
    }
    onExited: function(exitCode) { root.finishProbe(exitCode) }
  }

  Process {
    id: qrProcess
    command: []
    running: false
    stdinEnabled: true
    stdout: StdioCollector {
      id: qrStdout
      waitForEnd: true
    }
    stderr: StdioCollector { waitForEnd: true }
    onStarted: {
      write(root._qrPayload + "\n")
      root._qrPayload = ""
    }
    onExited: function(exitCode) {
      if (exitCode !== 0) {
        root.qrRows = []
        root.qrSize = 0
        return
      }
      var parsed = Model.parseQrOutput(qrStdout.text || "")
      root.qrRows = parsed.rows
      root.qrSize = parsed.size
    }
  }
}
