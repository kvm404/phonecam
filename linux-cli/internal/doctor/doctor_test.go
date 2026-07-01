package doctor

import (
	"bytes"
	"errors"
	"strings"
	"testing"
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
