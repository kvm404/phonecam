package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/start"
	"github.com/kvm404/phonecam/linux-cli/internal/trust"
)

type fakeSystem struct {
	paths       map[string]string
	exists      map[string]bool
	writable    map[string]bool
	files       map[string]string
	environment map[string]string
}

func (f fakeSystem) LookPath(name string) (string, error) {
	if path, ok := f.paths[name]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

func (f fakeSystem) RunCommand(name string, args ...string) error {
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

func (f fakeSystem) FileMode(path string) (os.FileMode, error) {
	return 0, os.ErrNotExist
}

func TestRunShowsHelpWithoutArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "phonecam <command>") {
		t.Fatalf("expected help output, got:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr, got:\n%s", stderr.String())
	}
}

func TestRunHelpListsSmokeCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "smoke      Run a local RTP loopback self-test") {
		t.Fatalf("expected smoke command in help, got:\n%s", stdout.String())
	}
}

func TestRunUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"wat"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command: wat") {
		t.Fatalf("expected unknown command output, got:\n%s", stderr.String())
	}
}

func TestRunDoctorReturnsFailureCodeWhenChecksFail(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"doctor"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "PhoneCam Doctor") {
		t.Fatalf("expected doctor output, got:\n%s", stdout.String())
	}
}

func TestParseStartFlagsDefaultsToFixedPorts(t *testing.T) {
	var stderr bytes.Buffer
	control, rtp, _, _, code, ok := parseStartFlags(nil, &stderr)
	if !ok {
		t.Fatalf("expected ok, got code %d", code)
	}
	if control != start.DefaultControlPort {
		t.Fatalf("expected default control port %d, got %d", start.DefaultControlPort, control)
	}
	if rtp != start.DefaultRTPPort {
		t.Fatalf("expected default RTP port %d, got %d", start.DefaultRTPPort, rtp)
	}
	if control != 47470 || rtp != 47471 {
		t.Fatalf("expected fixed ports 47470/47471, got %d/%d", control, rtp)
	}
}

func TestParseStartFlagsOverridesPorts(t *testing.T) {
	var stderr bytes.Buffer
	control, rtp, _, _, _, ok := parseStartFlags([]string{"--control-port", "9000", "--rtp-port", "9001"}, &stderr)
	if !ok {
		t.Fatal("expected ok")
	}
	if control != 9000 || rtp != 9001 {
		t.Fatalf("expected overridden ports 9000/9001, got %d/%d", control, rtp)
	}
}

func TestParseStartFlagsApprovalDefaults(t *testing.T) {
	var stderr bytes.Buffer

	_, _, auto, noTrust, _, ok := parseStartFlags(nil, &stderr)
	if !ok {
		t.Fatal("expected ok")
	}
	if !auto {
		t.Fatal("expected auto-approve to be the default")
	}
	if noTrust {
		t.Fatal("expected trust store to be used by default")
	}

	_, _, auto, _, _, ok = parseStartFlags([]string{"--require-approval"}, &stderr)
	if !ok {
		t.Fatal("expected ok")
	}
	if auto {
		t.Fatal("expected --require-approval to disable auto approval")
	}
}

func TestParseStartFlagsZeroMeansEphemeral(t *testing.T) {
	var stderr bytes.Buffer
	control, rtp, _, _, _, ok := parseStartFlags([]string{"--control-port=0", "--rtp-port=0"}, &stderr)
	if !ok {
		t.Fatal("expected ok")
	}
	if control != 0 || rtp != 0 {
		t.Fatalf("expected ephemeral ports 0/0, got %d/%d", control, rtp)
	}
}

func TestParseStartFlagsUnknownFlagFails(t *testing.T) {
	var stderr bytes.Buffer
	_, _, _, _, code, ok := parseStartFlags([]string{"--bogus"}, &stderr)
	if ok {
		t.Fatal("expected parse failure")
	}
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("expected usage/error on stderr")
	}
}

func TestRunStartUnknownFlagExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"start", "--bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("expected error on stderr")
	}
}

func TestRunTrustListAndRevoke(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"trust", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list empty: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No trusted phones") {
		t.Fatalf("expected empty list, got %q", stdout.String())
	}

	store, err := trust.Open(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert("id-1", "Pixel", time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	code = Run(nil, fakeSystem{}, []string{"trust", "list"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Pixel") {
		t.Fatalf("list: code=%d out=%q err=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	code = Run(nil, fakeSystem{}, []string{"trust", "revoke", "Pixel"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("revoke: %d %s", code, stderr.String())
	}
	stdout.Reset()
	code = Run(nil, fakeSystem{}, []string{"trust", "list"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "No trusted phones") {
		t.Fatalf("expected revoked, got %q", stdout.String())
	}
}

func TestParseStartFlagsNoTrust(t *testing.T) {
	var stderr bytes.Buffer
	_, _, _, noTrust, _, ok := parseStartFlags([]string{"--no-trust"}, &stderr)
	if !ok {
		t.Fatal("expected ok")
	}
	if !noTrust {
		t.Fatal("expected --no-trust")
	}
}

func TestHelpMentionsStartPortFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	Run(nil, fakeSystem{}, []string{"help"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "--control-port") || !strings.Contains(stdout.String(), "--rtp-port") {
		t.Fatalf("expected help to mention port flags, got:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "trust") || !strings.Contains(stdout.String(), "--no-trust") {
		t.Fatalf("expected help to mention trust, got:\n%s", stdout.String())
	}
}

func TestRunStatusRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"status", "extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "status takes no arguments") {
		t.Fatalf("expected usage error, got:\n%s", stderr.String())
	}
}

func TestRunStopRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"stop", "extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "stop takes no arguments") {
		t.Fatalf("expected usage error, got:\n%s", stderr.String())
	}
}

func TestInstallMentionsFirewallAndPersistence(t *testing.T) {
	var stdout, stderr bytes.Buffer
	Run(nil, fakeSystem{
		files: map[string]string{"/etc/os-release": "ID=arch\n"},
	}, []string{"install"}, &stdout, &stderr)
	output := stdout.String()
	for _, expected := range []string{
		"ufw allow 47470/tcp && sudo ufw allow 47471/udp",
		"firewall-cmd --permanent --add-port=47470/tcp --add-port=47471/udp",
		"echo v4l2loopback | sudo tee /etc/modules-load.d/v4l2loopback.conf",
		`echo "options v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1" | sudo tee /etc/modprobe.d/v4l2loopback.conf`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected install output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestRunInstallPrioritizesDetectedDistro(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{
		files: map[string]string{
			"/etc/os-release": "ID=arch\n",
		},
	}, []string{"install"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	output := stdout.String()
	if !strings.Contains(output, "gst-libav") {
		t.Fatalf("expected Arch libav package, got:\n%s", output)
	}
	if strings.Contains(output, "sudo dnf install") {
		t.Fatalf("expected only detected distro instructions, got:\n%s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr, got:\n%s", stderr.String())
	}
}
