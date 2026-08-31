// Pure helpers for the Omarchy PhoneCam plugin. No Qt. Node tests import
// this file the same way wifiqr and omaports do.

var BLOCKING_DOCTOR_NAMES = {
  "v4l2loopback install": true,
  "v4l2loopback module": true,
  "PhoneCam virtual camera": true
}

function isBlockingDoctorName(name) {
  return BLOCKING_DOCTOR_NAMES[String(name || "")] === true
}

function parseDoctor(text) {
  var lines = String(text || "").split(/\r?\n/)
  var checks = []
  var current = null
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i]
    var match = line.match(/^\[(FAIL|WARN|PASS|INFO)\]\s+(.+?):\s*(.*)$/)
    if (match) {
      current = { status: match[1], name: match[2], message: match[3], fix: "" }
      checks.push(current)
      continue
    }
    var fix = line.match(/^\s+Fix:\s+(.*)$/)
    if (fix && current) current.fix = fix[1]
  }
  return checks
}

function doctorBlocking(checks) {
  if (!(checks instanceof Array)) return false
  for (var i = 0; i < checks.length; i++) {
    var check = checks[i]
    if (check && check.status === "FAIL") return true
  }
  return false
}

function parseJsonObject(raw) {
  if (raw && typeof raw === "object" && !Array.isArray(raw)) return raw
  try {
    var value = JSON.parse(String(raw || ""))
    if (!value || typeof value !== "object" || Array.isArray(value)) return null
    return value
  } catch (error) {
    return null
  }
}

function stripSecrets(obj) {
  if (!obj || typeof obj !== "object") return obj
  var out = {}
  for (var key in obj) {
    if (key === "resume_token" || key === "pairing_secret") continue
    out[key] = obj[key]
  }
  return out
}

function parseStatus(raw) {
  var obj = parseJsonObject(raw)
  if (!obj) return null
  return stripSecrets(obj)
}

function parsePairing(raw) {
  return parseJsonObject(raw)
}

function publicPairing(pairing) {
  if (!pairing || typeof pairing !== "object") return null
  var out = {}
  for (var key in pairing) {
    if (key === "token") continue
    out[key] = pairing[key]
  }
  return out
}

function compactJson(value) {
  if (typeof value === "string") {
    var parsed = parseJsonObject(value)
    if (!parsed) return String(value).replace(/\s+/g, " ").trim()
    return JSON.stringify(parsed)
  }
  if (!value || typeof value !== "object") return ""
  return JSON.stringify(value)
}

function parseQrMatrix(lines) {
  if (!lines || lines.length === 0) return { rows: [], size: 0 }

  var size = lines[0].length
  if (size !== lines.length) return { rows: [], size: 0 }

  for (var i = 0; i < lines.length; i++) {
    if (lines[i].length !== size || !/^[01]+$/.test(lines[i])) return { rows: [], size: 0 }
  }

  return { rows: lines, size: size }
}

function parseQrOutput(raw) {
  var lines = String(raw || "").trim().split(/\r?\n/).filter(function(line) { return line !== "" })
  return parseQrMatrix(lines)
}

function parseExpiresMs(expires) {
  if (expires == null || expires === "") return 0
  var t = Date.parse(String(expires))
  return isFinite(t) ? t : 0
}

function pairingExpired(pairing, nowMs) {
  var exp = parseExpiresMs(pairing && pairing.expires)
  if (!exp) return false
  var now = nowMs == null ? Date.now() : nowMs
  return now >= exp
}

function derivePhase(input) {
  input = input || {}
  if (!input.binary) return "missing-binary"
  if (input.crash && !input.healthz && !input.startRunning) return "error"
  if (input.startRunning && !input.healthz) return "starting"
  if (input.healthz && !input.approved) return "waiting"
  if (input.approved) {
    var ms = input.last_rtp_ms
    if (typeof ms === "number" && isFinite(ms) && ms < 2000) return "live"
    return "silent"
  }
  if (input.startRunning) return "starting"
  return "stopped"
}

function normalizePort(value, fallback) {
  var n = parseInt(value, 10)
  if (!isFinite(n) || n < 1024 || n > 65535) return fallback
  return n
}

function binaryCandidates(binaryPath, home, goPath) {
  var out = []
  var seen = {}
  function add(path) {
    var value = String(path || "").trim()
    if (value === "" || seen[value]) return
    seen[value] = true
    out.push(value)
  }
  add(binaryPath)
  add("phonecam")
  if (home) add(String(home).replace(/\/+$/, "") + "/.local/bin/phonecam")
  if (goPath) add(String(goPath).replace(/\/+$/, "") + "/bin/phonecam")
  return out
}

function loopbackUrl(port, path) {
  return "http://127.0.0.1:" + port + path
}

function curlArgs(method, url) {
  var args = ["curl", "-sS", "--max-time", "2"]
  if (method && method !== "GET") args.push("-X", method)
  args.push(url)
  return args
}

function startArgv(bin, controlPort, rtpPort) {
  return [
    "setpriv", "--pdeathsig", "TERM",
    bin, "start",
    "--control-port", String(controlPort),
    "--rtp-port", String(rtpPort)
  ]
}

function concise(text) {
  var value = String(text || "").replace(/\s+/g, " ").trim()
  return value.length > 180 ? value.substring(0, 177) + "..." : value
}

function safeTrustId(id) {
  var value = String(id || "")
  if (!/^[A-Za-z0-9._:-]+$/.test(value)) return ""
  return value
}

function videoLabel(video) {
  if (!video || typeof video !== "object") return ""
  var width = video.width
  var height = video.height
  if (!width || !height) return ""
  var fps = video.fps ? " " + video.fps + "fps" : ""
  return width + "x" + height + fps
}

function phaseLabel(phase) {
  if (phase === "stopped") return "STOPPED"
  if (phase === "missing-binary") return "NO BINARY"
  if (phase === "starting") return "STARTING"
  if (phase === "waiting") return "WAITING"
  if (phase === "live") return "LIVE"
  if (phase === "silent") return "SILENT"
  if (phase === "error") return "ERROR"
  return String(phase || "").toUpperCase()
}

function parseTrust(raw) {
  var obj = parseJsonObject(raw)
  if (!obj || !(obj.phones instanceof Array)) return []
  var phones = []
  for (var i = 0; i < obj.phones.length; i++) {
    var phone = obj.phones[i]
    if (!phone || typeof phone !== "object") continue
    phones.push({
      id: String(phone.id || ""),
      name: String(phone.name || ""),
      created_at: phone.created_at || "",
      last_seen: phone.last_seen || ""
    })
  }
  return phones
}

function parseSessionRecord(raw) {
  var obj = parseJsonObject(raw)
  if (!obj) return null
  var control = normalizePort(obj.control_port, 0)
  var rtp = normalizePort(obj.rtp_port, 0)
  if (!control) return null
  return {
    pid: parseInt(obj.pid, 10) || 0,
    control_port: control,
    rtp_port: rtp,
    session: String(obj.session || "")
  }
}

if (typeof module !== "undefined") {
  module.exports = {
    BLOCKING_DOCTOR_NAMES: BLOCKING_DOCTOR_NAMES,
    isBlockingDoctorName: isBlockingDoctorName,
    parseDoctor: parseDoctor,
    doctorBlocking: doctorBlocking,
    parseStatus: parseStatus,
    parsePairing: parsePairing,
    publicPairing: publicPairing,
    compactJson: compactJson,
    parseQrMatrix: parseQrMatrix,
    parseQrOutput: parseQrOutput,
    pairingExpired: pairingExpired,
    derivePhase: derivePhase,
    normalizePort: normalizePort,
    binaryCandidates: binaryCandidates,
    loopbackUrl: loopbackUrl,
    curlArgs: curlArgs,
    startArgv: startArgv,
    concise: concise,
    safeTrustId: safeTrustId,
    videoLabel: videoLabel,
    phaseLabel: phaseLabel,
    parseTrust: parseTrust,
    parseSessionRecord: parseSessionRecord
  }
}
