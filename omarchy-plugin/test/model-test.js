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

test("holdersHeadline names Discord and ignores the gst receiver", function() {
  const checks = Model.parseDoctor([
    "[WARN] Virtual camera holders: /dev/video10 is already open by gst-launch-1.0, Discord",
    "      Fix: Close those apps.",
    "[PASS] Distro: Arch-family distro detected"
  ].join("\n"))
  const holders = Model.parseHolders(checks)
  assert.strictEqual(holders.length, 2)
  assert.strictEqual(holders[0].kind, "receiver")
  assert.strictEqual(holders[1].label, "Discord")
  assert.strictEqual(Model.holdersHeadline(holders), "In use: Discord")
  assert.strictEqual(Model.holdersHeadline([
    { kind: "receiver", label: "PhoneCam receiver" }
  ]), "Ready — pick PhoneCam in an app")
  assert.strictEqual(Model.holdersHeadline([]), "No app is using PhoneCam")
  assert.strictEqual(Model.classifyHolder("chromium").label, "Chromium")
  assert.strictEqual(Model.classifyHolder("chrome").label, "Chrome")
  assert.strictEqual(Model.classifyHolder("pipewire").kind, "receiver")
  assert.strictEqual(Model.holdersHeadline([
    { kind: "unknown", label: "" }
  ]), "Could not see who holds PhoneCam")
})

console.log("all tests passed")
