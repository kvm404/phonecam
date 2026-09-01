#!/usr/bin/env node
"use strict"

const assert = require("assert")
const path = require("path")
const Model = require(path.join(__dirname, "..", "Model.js"))

function test(name, fn) {
  fn()
  console.log("ok  " + name)
}

test("parseDoctor FAIL line plus Fix", function() {
  const report = [
    "PhoneCam Doctor",
    "",
    "[FAIL] v4l2loopback install: missing",
    "      Fix: Install v4l2loopback for your kernel, then run doctor again.",
    "[WARN] Virtual camera holders: ffmpeg",
    "      Fix: Close leftover readers on /dev/video10.",
    "[PASS] Distro: Arch-family distro detected",
    "",
    "Result: issues found",
    ""
  ].join("\n")
  const checks = Model.parseDoctor(report)
  assert.strictEqual(checks.length, 3)
  assert.deepStrictEqual(checks[0], {
    status: "FAIL",
    name: "v4l2loopback install",
    message: "missing",
    fix: "Install v4l2loopback for your kernel, then run doctor again."
  })
  assert.strictEqual(checks[1].status, "WARN")
  assert.strictEqual(checks[1].name, "Virtual camera holders")
})

test("blocking names are an exact list", function() {
  const names = Object.keys(Model.BLOCKING_DOCTOR_NAMES).sort()
  assert.deepStrictEqual(names, [
    "PhoneCam virtual camera",
    "v4l2loopback install",
    "v4l2loopback module"
  ])
  assert.strictEqual(Model.isBlockingDoctorName("v4l2loopback install"), true)
  assert.strictEqual(Model.isBlockingDoctorName("v4l2loopback module"), true)
  assert.strictEqual(Model.isBlockingDoctorName("PhoneCam virtual camera"), true)
  assert.strictEqual(Model.isBlockingDoctorName("Virtual camera holders"), false)
  assert.strictEqual(Model.isBlockingDoctorName("PhoneCam virtual camera identity"), false)
  assert.strictEqual(Model.doctorBlocking([
    { status: "FAIL", name: "GStreamer launcher" }
  ]), true)
  assert.strictEqual(Model.doctorBlocking([
    { status: "WARN", name: "v4l2loopback module" }
  ]), false)
  assert.strictEqual(Model.doctorBlocking([
    { status: "FAIL", name: "v4l2loopback module" }
  ]), true)
  assert.strictEqual(Model.doctorBlocking([
    { status: "FAIL", name: "Virtual camera holders" }
  ]), true)
})

test("parseStatus strips resume_token and pairing_secret", function() {
  const status = Model.parseStatus(JSON.stringify({
    ok: true,
    approved: true,
    phone_name: "Pixel",
    resume_token: "secret-resume",
    pairing_secret: "secret-pair",
    last_rtp_ms: 12
  }))
  assert.strictEqual(status.ok, true)
  assert.strictEqual(status.phone_name, "Pixel")
  assert.strictEqual(status.resume_token, undefined)
  assert.strictEqual(status.pairing_secret, undefined)
  assert.ok(!("resume_token" in status))
  assert.ok(!("pairing_secret" in status))
})

test("parsePairing keeps expires; compactJson is not pretty-printed", function() {
  const raw = JSON.stringify({
    v: 1,
    name: "laptop",
    control: "http://10.0.0.4:47470",
    rtp: "10.0.0.4:47471",
    session: "abc",
    token: "tok",
    expires: "2026-08-31T12:00:00Z",
    transport: "rtp/h264",
    video: { width: 1280, height: 720, fps: 30 }
  }, null, 2)
  assert.ok(raw.indexOf("\n") !== -1)
  const pairing = Model.parsePairing(raw)
  assert.strictEqual(pairing.expires, "2026-08-31T12:00:00Z")
  assert.strictEqual(pairing.token, "tok")
  const compact = Model.compactJson(pairing)
  assert.strictEqual(compact.indexOf("\n"), -1)
  assert.strictEqual(compact.indexOf("  "), -1)
  assert.strictEqual(JSON.parse(compact).token, "tok")
  assert.strictEqual(Model.publicPairing(pairing).token, undefined)
  assert.strictEqual(Model.publicPairing(pairing).expires, "2026-08-31T12:00:00Z")
})

test("parseQrMatrix square ok / ragged fail", function() {
  const ok = Model.parseQrMatrix(["010", "101", "010"])
  assert.strictEqual(ok.size, 3)
  assert.deepStrictEqual(ok.rows, ["010", "101", "010"])
  assert.strictEqual(Model.parseQrMatrix(["01", "0"]).size, 0)
  assert.strictEqual(Model.parseQrMatrix(["0a", "10"]).size, 0)
  assert.strictEqual(Model.parseQrMatrix(["01", "011"]).size, 0)
  assert.strictEqual(Model.parseQrMatrix([]).size, 0)
})

test("phase derivation table", function() {
  const table = [
    [{}, "missing-binary"],
    [{ binary: true }, "stopped"],
    [{ binary: true, startRunning: true }, "starting"],
    [{ binary: true, startRunning: true, healthz: true }, "waiting"],
    [{ binary: true, healthz: true, approved: false }, "waiting"],
    [{ binary: true, healthz: true, approved: true, last_rtp_ms: 0 }, "live"],
    [{ binary: true, healthz: true, approved: true, last_rtp_ms: 1999 }, "live"],
    [{ binary: true, healthz: true, approved: true, last_rtp_ms: 2000 }, "silent"],
    [{ binary: true, healthz: true, approved: true, last_rtp_ms: null }, "silent"],
    [{ binary: true, healthz: true, approved: true }, "silent"],
    [{ binary: true, crash: true }, "error"],
    [{ binary: true, crash: true, healthz: true, approved: false }, "waiting"]
  ]
  for (var i = 0; i < table.length; i++) {
    const got = Model.derivePhase(table[i][0])
    assert.strictEqual(got, table[i][1], JSON.stringify(table[i][0]) + " -> " + got)
  }
})

test("scanPendingApproval is not part of the model", function() {
  assert.strictEqual(typeof Model.scanPendingApproval, "undefined")
})

test("pairingExpired uses expires", function() {
  const pairing = { expires: "2026-08-31T12:00:00.000Z" }
  assert.strictEqual(Model.pairingExpired(pairing, Date.parse("2026-08-31T11:59:59Z")), false)
  assert.strictEqual(Model.pairingExpired(pairing, Date.parse("2026-08-31T12:00:00Z")), true)
})

test("startArgv never includes --require-approval", function() {
  const argv = Model.startArgv("/bin/phonecam", 47470, 47471)
  assert.deepStrictEqual(argv, [
    "setpriv", "--pdeathsig", "TERM",
    "/bin/phonecam", "start",
    "--control-port", "47470",
    "--rtp-port", "47471"
  ])
  assert.ok(argv.indexOf("--require-approval") === -1)
})

test("parseSessionRecord keeps device path", function() {
  const rec = Model.parseSessionRecord(JSON.stringify({
    pid: 12,
    control_port: 47470,
    rtp_port: 47471,
    session: "abc",
    device: "/dev/video10"
  }))
  assert.strictEqual(rec.device, "/dev/video10")
})

test("previewHelperArgv pins /dev/video10 and never video0", function() {
  const argv = Model.previewHelperArgv(
    "/opt/plugin/bin/phonecam-preview",
    "/dev/video10",
    "/run/user/1000/phonecam"
  )
  assert.deepStrictEqual(argv, [
    "setpriv", "--pdeathsig", "TERM",
    "/opt/plugin/bin/phonecam-preview",
    "/dev/video10",
    "/run/user/1000/phonecam"
  ])
  assert.ok(argv.join(" ").indexOf("video0") === -1)
  assert.deepStrictEqual(Model.previewHelperArgv("/opt/plugin/bin/phonecam-preview", "/dev/video10", "../tmp"), [])
  assert.strictEqual(Model.previewFramePath("/run/user/1000", 1), "/run/user/1000/phonecam/frame-1.jpg")
  assert.strictEqual(Model.v4l2Device("/dev/video10"), "/dev/video10")
  assert.strictEqual(Model.v4l2Device("/dev/../video10"), "")
})

test("pickCameraDevice prefers PhoneCam label then video_nr", function() {
  const inputs = [
    { id: "/dev/video0", description: "HP TrueVision" },
    { id: "/dev/video10", description: "PhoneCam" }
  ]
  const hit = Model.pickCameraDevice(inputs, "/dev/video10", "PhoneCam")
  assert.strictEqual(hit.description, "PhoneCam")
  const byPath = Model.pickCameraDevice(
    [{ id: "v4l2:/dev/video10", description: "Dummy" }],
    "/dev/video10",
    "PhoneCam"
  )
  assert.strictEqual(byPath.id, "v4l2:/dev/video10")
  assert.strictEqual(Model.pickCameraDevice(inputs, "", "Nope"), null)
})

console.log("all tests passed")
