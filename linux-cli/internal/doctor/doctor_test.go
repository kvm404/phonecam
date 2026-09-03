package doctor

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
)

type fakeSystem struct {
	paths           map[string]string
	commandFailures map[string]bool
	commandOutput   map[string]string
	exists          map[string]bool
	writable        map[string]bool
	files           map[string]string
	environment     map[string]string
	modes           map[string]os.FileMode
	openers         []string
	openersErr      error
}

func (f fakeSystem) LookPath(name string) (string, error) {
	if path, ok := f.paths[name]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

func (f fakeSystem) RunCommand(name string, args ...string) error {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	if f.commandFailures[key] {
		return errors.New("command failed")
	}
	return nil
}

func (f fakeSystem) CommandOutput(name string, args ...string) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	if f.commandFailures[key] {
		out := f.commandOutput[key]
		return []byte(out), errors.New("command failed")
	}
	if f.commandOutput != nil {
		if out, ok := f.commandOutput[key]; ok {
			return []byte(out), nil
		}
	}
	return nil, nil
}

func (f fakeSystem) Exists(path string) bool {
	return f.exists[path]
}

func (f fakeSystem) CanWrite(path string) bool {
	return f.writable[path]
}

func (f fakeSystem) ReadFile(path string) ([]byte, error) {
	if content, ok := f.files[path]; ok {
		return []byte(content), nil
	}
	return nil, errors.New("not found")
}

func (f fakeSystem) Getenv(name string) string {
	return f.environment[name]
}

func (f fakeSystem) FileMode(path string) (os.FileMode, error) {
	if f.modes != nil {
		if mode, ok := f.modes[path]; ok {
			return mode, nil
		}
	}
	return 0, os.ErrNotExist
}

func (f fakeSystem) DeviceOpeners(path string) ([]string, error) {
	return f.openers, f.openersErr
}

func TestDoctorReportsReadyWhenCoreChecksPass(t *testing.T) {
	report := Run(readySystem(fakeSystem{}))

	if report.HasFailures() {
		t.Fatalf("expected no failures: %#v", report.Checks)
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	if !strings.Contains(out.String(), "Result: ready") {
		t.Fatalf("expected ready output, got:\n%s", out.String())
	}

	caps := findCheck(t, report, "v4l2loopback exclusive_caps")
	if caps.Status != StatusPass {
		t.Fatalf("expected exclusive_caps PASS on readySystem, got %s (%s)", caps.Status, caps.Message)
	}
	holders := findCheck(t, report, "Virtual camera holders")
	if holders.Status != StatusPass {
		t.Fatalf("expected holders PASS on readySystem, got %s (%s)", holders.Status, holders.Message)
	}
}

func TestDoctorFailsWhenVirtualCameraIsMissing(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		exists: map[string]bool{
			"/sys/module/v4l2loopback": true,
		},
		files: map[string]string{
			"/etc/os-release": "ID=ubuntu\nID_LIKE=debian\n",
		},
	}))

	if !report.HasFailures() {
		t.Fatal("expected missing virtual camera to fail doctor")
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	output := out.String()
	if !strings.Contains(output, "/dev/video10 does not exist") {
		t.Fatalf("expected missing device message, got:\n%s", output)
	}
}

func TestDoctorFailsWhenRequiredGStreamerElementIsMissing(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		commandFailures: map[string]bool{
			"gst-inspect-1.0 avdec_h264": true,
		},
	}))

	if !report.HasFailures() {
		t.Fatal("expected missing avdec_h264 to fail doctor")
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	output := out.String()
	if !strings.Contains(output, "GStreamer element avdec_h264") {
		t.Fatalf("expected avdec_h264 check, got:\n%s", output)
	}
	if !strings.Contains(output, "Install the GStreamer libav plugin") {
		t.Fatalf("expected libav remediation, got:\n%s", output)
	}
}

func TestDoctorFailsWhenVideo10IsNotPhoneCam(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		files: map[string]string{
			"/sys/class/video4linux/video10/name": "Integrated Camera\n",
		},
	}))

	if !report.HasFailures() {
		t.Fatal("expected conflicting /dev/video10 to fail doctor")
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	if !strings.Contains(out.String(), "expected PhoneCam") {
		t.Fatalf("expected identity failure, got:\n%s", out.String())
	}
}

func TestDoctorFailsWhenVirtualCameraIsNotWritable(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		writable: map[string]bool{
			"/dev/video10": false,
		},
	}))

	if !report.HasFailures() {
		t.Fatal("expected non-writable virtual camera to fail doctor")
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	if !strings.Contains(out.String(), "not writable") {
		t.Fatalf("expected permission failure, got:\n%s", out.String())
	}
}

func TestDoctorFailsWhenV4L2LoopbackIsNotInstalled(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		commandFailures: map[string]bool{
			"modinfo v4l2loopback": true,
		},
	}))

	if !report.HasFailures() {
		t.Fatal("expected missing v4l2loopback install to fail doctor")
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	if !strings.Contains(out.String(), "v4l2loopback module metadata was not found") {
		t.Fatalf("expected v4l2loopback install failure, got:\n%s", out.String())
	}
}

func TestDoctorWarnsForUnknownDistro(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		files: map[string]string{
			"/etc/os-release":                     "ID=nixos\n",
			"/sys/class/video4linux/video10/name": "PhoneCam\n",
		},
	}))

	if report.HasFailures() {
		t.Fatalf("expected unknown distro warning without failure: %#v", report.Checks)
	}

	var out bytes.Buffer
	WriteReport(&out, report)
	if !strings.Contains(out.String(), "unsupported or untested distro: nixos") {
		t.Fatalf("expected unknown distro warning, got:\n%s", out.String())
	}
}

func findCheck(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q not found in report", name)
	return Check{}
}

func TestFirewallCheckWarnsWhenUFWActive(t *testing.T) {
	// readySystem's RunCommand succeeds for everything, so `systemctl is-active
	// --quiet ufw` reports active.
	report := Run(readySystem(fakeSystem{}))
	check := findCheck(t, report, "Firewall")
	if check.Status != StatusWarn {
		t.Fatalf("expected WARN for active ufw, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "ufw is active") {
		t.Fatalf("expected ufw message, got %q", check.Message)
	}
	if !strings.Contains(check.Fix, "sudo ufw allow 47470/tcp && sudo ufw allow 47471/udp") {
		t.Fatalf("expected ufw allow fix, got %q", check.Fix)
	}
	if report.HasFailures() {
		t.Fatal("firewall warning must not fail the report")
	}
}

func TestFirewallCheckWarnsWhenFirewalldActive(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		commandFailures: map[string]bool{
			"systemctl is-active --quiet ufw":     true,
			"firewall-cmd --query-port=47470/tcp": true,
			"firewall-cmd --query-port=47471/udp": true,
		},
	}))
	check := findCheck(t, report, "Firewall")
	if check.Status != StatusWarn {
		t.Fatalf("expected WARN for active firewalld, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "firewalld is active") {
		t.Fatalf("expected firewalld message, got %q", check.Message)
	}
	if !strings.Contains(check.Fix, "firewall-cmd --permanent --add-port=47470/tcp --add-port=47471/udp") {
		t.Fatalf("expected firewalld allow fix, got %q", check.Fix)
	}
}

func TestFirewallCheckInfoWhenFirewalldAlreadyAllows(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		commandFailures: map[string]bool{
			"systemctl is-active --quiet ufw": true,
		},
	}))
	check := findCheck(t, report, "Firewall")
	if check.Status != StatusInfo {
		t.Fatalf("expected INFO when firewalld already allows PhoneCam ports, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "already allows PhoneCam ports") {
		t.Fatalf("expected already-allows message, got %q", check.Message)
	}
	if check.Fix != "" {
		t.Fatalf("expected no firewall fix when ports are already allowed, got %q", check.Fix)
	}
}

func TestFirewallCheckInfoWhenNoFirewall(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		commandFailures: map[string]bool{
			"systemctl is-active --quiet ufw":       true,
			"systemctl is-active --quiet firewalld": true,
		},
	}))
	check := findCheck(t, report, "Firewall")
	if check.Status != StatusInfo {
		t.Fatalf("expected INFO when no firewall active, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "no active ufw or firewalld") {
		t.Fatalf("expected no-firewall message, got %q", check.Message)
	}
}

func TestUDPRcvbufCheckIsPresent(t *testing.T) {
	report := Run(readySystem(fakeSystem{}))
	check := findCheck(t, report, "UDP receive buffer")
	if check.Status != StatusPass && check.Status != StatusWarn {
		t.Fatalf("expected PASS or WARN for UDP receive buffer, got %s (%s)", check.Status, check.Message)
	}
	if report.HasFailures() && check.Status == StatusWarn {
		t.Fatal("rcvbuf warning must not fail the report")
	}
}

func TestRcvbufEffectiveWarnsBelow4MB(t *testing.T) {
	if rcvbufEffective(rtp.PublicRcvbuf*2) < rtp.PublicRcvbuf {
		t.Fatal("doubled 4MB getsockopt value should pass")
	}
	if rcvbufEffective(208*1024) >= rtp.PublicRcvbuf {
		t.Fatal("default kernel rmem should warn")
	}
}

func TestOSReleaseValueHandlesQuotes(t *testing.T) {
	data := "ID=manjaro\nID_LIKE=\"arch linux\"\n"
	if got := osReleaseValue(data, "ID_LIKE"); got != "arch linux" {
		t.Fatalf("expected quoted value to be unquoted, got %q", got)
	}
}

func TestDistroFamily(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "arch ID", files: map[string]string{"/etc/os-release": "ID=arch\n"}, want: "arch"},
		{name: "arch ID_LIKE", files: map[string]string{"/etc/os-release": "ID=manjaro\nID_LIKE=\"arch linux\"\n"}, want: "arch"},
		{name: "fedora ID", files: map[string]string{"/etc/os-release": "ID=fedora\nVERSION_ID=41\n"}, want: "fedora"},
		{name: "fedora ID_LIKE", files: map[string]string{"/etc/os-release": "ID=nobara\nID_LIKE=fedora\n"}, want: "fedora"},
		{name: "ubuntu", files: map[string]string{"/etc/os-release": "ID=ubuntu\nID_LIKE=debian\n"}, want: "debian"},
		{name: "debian", files: map[string]string{"/etc/os-release": "ID=debian\n"}, want: "debian"},
		{name: "unknown id", files: map[string]string{"/etc/os-release": "ID=nixos\n"}, want: "nixos"},
		{name: "empty id", files: map[string]string{"/etc/os-release": "NAME=Empty\n"}, want: "unknown"},
		{name: "unreadable os-release", files: nil, want: "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DistroFamily(fakeSystem{files: tc.files})
			if got != tc.want {
				t.Fatalf("DistroFamily=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirewallCheckInfoWhenUFWAlreadyAllows(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		commandOutput: map[string]string{
			"ufw status": `Status: active

To                         Action      From
--                         ------      ----
47470/tcp                  ALLOW       Anywhere
47471/udp                  ALLOW       Anywhere
`,
		},
	}))
	check := findCheck(t, report, "Firewall")
	if check.Status != StatusInfo {
		t.Fatalf("expected INFO when ufw already allows PhoneCam ports, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "already allows PhoneCam ports") {
		t.Fatalf("expected already-allows message, got %q", check.Message)
	}
	if check.Fix != "" {
		t.Fatalf("expected no firewall fix when ports are already allowed, got %q", check.Fix)
	}
}

func TestUFWStatusAllowsPhoneCam(t *testing.T) {
	if UFWStatusAllowsPhoneCam("") {
		t.Fatal("empty status must not look allowed")
	}
	allowed := `Status: active
47470/tcp                  ALLOW       Anywhere
47471/udp                  ALLOW       Anywhere
`
	if !UFWStatusAllowsPhoneCam(allowed) {
		t.Fatal("expected both ports ALLOW to count as allowed")
	}
	tcpOnly := "47470/tcp ALLOW Anywhere\n"
	if UFWStatusAllowsPhoneCam(tcpOnly) {
		t.Fatal("tcp-only must not count as allowed")
	}
}

func readySystem(overrides fakeSystem) fakeSystem {
	base := fakeSystem{
		paths: map[string]string{
			"gst-launch-1.0":  "/usr/bin/gst-launch-1.0",
			"gst-inspect-1.0": "/usr/bin/gst-inspect-1.0",
			"modprobe":        "/usr/sbin/modprobe",
			"modinfo":         "/usr/sbin/modinfo",
		},
		exists: map[string]bool{
			"/sys/module/v4l2loopback": true,
			"/dev/video10":             true,
		},
		writable: map[string]bool{
			"/dev/video10": true,
		},
		files: map[string]string{
			"/etc/os-release":                                    "ID=arch\n",
			"/sys/class/video4linux/video10/name":                "PhoneCam\n",
			"/sys/module/v4l2loopback/parameters/video_nr":       "10,-1,-1,-1,-1,-1,-1,-1\n",
			"/sys/module/v4l2loopback/parameters/exclusive_caps": "Y,N,N,N,N,N,N,N\n",
		},
		environment: map[string]string{
			"XDG_SESSION_TYPE":    "wayland",
			"XDG_CURRENT_DESKTOP": "Hyprland",
		},
	}

	if overrides.paths != nil {
		base.paths = overrides.paths
	}
	if overrides.commandFailures != nil {
		base.commandFailures = overrides.commandFailures
	}
	if overrides.commandOutput != nil {
		base.commandOutput = overrides.commandOutput
	}
	if overrides.exists != nil {
		base.exists = overrides.exists
	}
	if overrides.writable != nil {
		base.writable = overrides.writable
	}
	if overrides.files != nil {
		for key, value := range overrides.files {
			base.files[key] = value
		}
	}
	if overrides.environment != nil {
		for key, value := range overrides.environment {
			base.environment[key] = value
		}
	}
	if overrides.modes != nil {
		base.modes = overrides.modes
	}
	if overrides.openers != nil {
		base.openers = overrides.openers
	}
	base.openersErr = overrides.openersErr
	return base
}

func TestLANPrivacyMentionsUnencryptedRTPAndLocalTrust(t *testing.T) {
	report := Run(readySystem(fakeSystem{}))
	check := findCheck(t, report, "LAN privacy")
	if check.Status != StatusInfo {
		t.Fatalf("expected INFO, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "without payload encryption") {
		t.Fatalf("LAN privacy must stay honest about unencrypted RTP, got %q", check.Message)
	}
	if !strings.Contains(check.Message, "trusted pairing does not encrypt RTP") {
		t.Fatalf("expected local trust disclosure, got %q", check.Message)
	}
}

func TestDoctorFailsWhenExclusiveCapsDisabledOnVideo10(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		files: map[string]string{
			"/sys/module/v4l2loopback/parameters/video_nr":       "0,10,-1,-1,-1,-1,-1,-1\n",
			"/sys/module/v4l2loopback/parameters/exclusive_caps": "Y,N,N,N,N,N,N,N\n",
		},
	}))
	if !report.HasFailures() {
		t.Fatal("expected exclusive_caps=N on video10 to fail doctor")
	}
	check := findCheck(t, report, "v4l2loopback exclusive_caps")
	if check.Status != StatusFail {
		t.Fatalf("expected FAIL, got %s (%s)", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "exclusive_caps disabled") {
		t.Fatalf("expected disabled message, got %q", check.Message)
	}
	if !strings.Contains(check.Fix, "exclusive_caps=1,1") {
		t.Fatalf("expected per-device exclusive_caps=1,1 fix, got %q", check.Fix)
	}
}

func TestDoctorPassesWhenExclusiveCapsEnabledOnVideo10(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		files: map[string]string{
			"/sys/module/v4l2loopback/parameters/video_nr":       "0,10,-1,-1,-1,-1,-1,-1\n",
			"/sys/module/v4l2loopback/parameters/exclusive_caps": "N,Y,N,N,N,N,N,N\n",
		},
	}))
	if report.HasFailures() {
		t.Fatalf("expected exclusive_caps enabled to pass: %#v", report.Checks)
	}
	check := findCheck(t, report, "v4l2loopback exclusive_caps")
	if check.Status != StatusPass {
		t.Fatalf("expected PASS, got %s (%s)", check.Status, check.Message)
	}
}

func TestDoctorDoesNotFailWhenExclusiveCapsSlotNotFound(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		files: map[string]string{
			"/sys/module/v4l2loopback/parameters/video_nr":       "0,-1,-1,-1,-1,-1,-1,-1\n",
			"/sys/module/v4l2loopback/parameters/exclusive_caps": "Y,N,N,N,N,N,N,N\n",
		},
	}))
	if report.HasFailures() {
		t.Fatalf("unknown exclusive_caps slot must not fail doctor: %#v", report.Checks)
	}
	check := findCheck(t, report, "v4l2loopback exclusive_caps")
	if check.Status == StatusFail {
		t.Fatalf("expected no FAIL when video10 slot is missing, got %s (%s)", check.Status, check.Message)
	}
}

func TestDoctorWarnsWhenExclusiveCapsUnreadable(t *testing.T) {
	sys := readySystem(fakeSystem{})
	delete(sys.files, "/sys/module/v4l2loopback/parameters/video_nr")
	delete(sys.files, "/sys/module/v4l2loopback/parameters/exclusive_caps")
	report := Run(sys)
	if report.HasFailures() {
		t.Fatalf("missing exclusive_caps files must not fail doctor: %#v", report.Checks)
	}
	check := findCheck(t, report, "v4l2loopback exclusive_caps")
	if check.Status != StatusWarn {
		t.Fatalf("expected WARN when exclusive_caps is unreadable, got %s (%s)", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "could not read exclusive_caps") {
		t.Fatalf("expected unreadable message, got %q", check.Message)
	}
}

func TestDoctorWarnsWhenVirtualCameraHoldersPresent(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		openers: []string{"gst-launch-1.0", "pipewire"},
	}))
	if report.HasFailures() {
		t.Fatal("leftover holders must not fail doctor")
	}
	check := findCheck(t, report, "Virtual camera holders")
	if check.Status != StatusWarn {
		t.Fatalf("expected WARN for leftover holders, got %s (%s)", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "gst-launch-1.0") || !strings.Contains(check.Message, "pipewire") {
		t.Fatalf("expected holder process names, got %q", check.Message)
	}
	if !strings.Contains(check.Fix, "fuser -k /dev/video10") || !strings.Contains(check.Fix, "phonecam stop") {
		t.Fatalf("expected fuser/stop fix, got %q", check.Fix)
	}
}

func TestTrustFileWarnsWhenModeIsNot0600(t *testing.T) {
	report := Run(readySystem(fakeSystem{
		environment: map[string]string{
			"XDG_CONFIG_HOME": "/xdg",
		},
		modes: map[string]os.FileMode{
			"/xdg/phonecam/trusted.json": 0o644,
		},
	}))
	check := findCheck(t, report, "Trust file")
	if check.Status != StatusWarn {
		t.Fatalf("expected WARN for 0644 trust file, got %s (%s)", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "0644") {
		t.Fatalf("expected mode in message, got %q", check.Message)
	}
}
