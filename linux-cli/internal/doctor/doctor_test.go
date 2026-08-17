package doctor

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
)

type fakeSystem struct {
	paths           map[string]string
	commandFailures map[string]bool
	exists          map[string]bool
	writable        map[string]bool
	files           map[string]string
	environment     map[string]string
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
			"systemctl is-active --quiet ufw": true,
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
			"/etc/os-release":                     "ID=arch\n",
			"/sys/class/video4linux/video10/name": "PhoneCam\n",
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
		base.environment = overrides.environment
	}
	return base
}
