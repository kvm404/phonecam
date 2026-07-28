package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kvm404/phonecam/linux-cli/internal/doctor"
	"github.com/kvm404/phonecam/linux-cli/internal/lifecycle"
	"github.com/kvm404/phonecam/linux-cli/internal/smoke"
	"github.com/kvm404/phonecam/linux-cli/internal/start"
)

var version = "0.1.0"

const writeAccess = 2

type OSSystem struct{}

func (OSSystem) LookPath(name string) (string, error) {
	path, err := findExecutable(name)
	if err == nil {
		return path, nil
	}
	return "", err
}

func (OSSystem) RunCommand(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func (OSSystem) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (OSSystem) CanWrite(path string) bool {
	return syscall.Access(path, writeAccess) == nil
}

func (OSSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (OSSystem) Getenv(name string) string {
	return os.Getenv(name)
}

func Run(ctx context.Context, sys doctor.System, args []string, stdout, stderr io.Writer) int {
	_ = ctx

	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}

	switch args[0] {
	case "-h", "--help", "help":
		printHelp(stdout)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintf(stdout, "phonecam %s\n", version)
		return 0
	case "doctor":
		report := doctor.Run(sys)
		doctor.WriteReport(stdout, report)
		if report.HasFailures() {
			return 1
		}
		return 0
	case "start":
		return runStart(ctx, args[1:], stdout, stderr)
	case "smoke":
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := smoke.New(nil, nil, nil, nil, nil, nil).Run(runCtx, smoke.Config{Device: smoke.DefaultDevice}, stdout)
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "phonecam smoke failed: %v\n", err)
			return 1
		}
		return 0
	case "status":
		if code, ok := requireNoArgs("status", args[1:], stderr); !ok {
			return code
		}
		return lifecycle.NewManager(nil, nil, nil).Status(stdout, stderr)
	case "stop":
		if code, ok := requireNoArgs("stop", args[1:], stderr); !ok {
			return code
		}
		return lifecycle.NewManager(nil, nil, nil).Stop(stdout, stderr)
	case "install":
		printInstall(sys, stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return 2
	}
}

// requireNoArgs enforces that a command took no extra arguments. When args are
// present it prints a usage error and returns (2, false) so the caller exits.
func requireNoArgs(command string, args []string, stderr io.Writer) (int, bool) {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "phonecam %s takes no arguments\n", command)
		return 2, false
	}
	return 0, true
}

func runStart(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	controlPort, rtpPort, autoApprove, code, ok := parseStartFlags(args, stderr)
	if !ok {
		return code
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := start.New(start.OSSystem{}, nil, nil, nil).Run(runCtx, start.Config{
		VirtualCamera: start.DefaultVirtualCamera,
		ControlPort:   controlPort,
		RTPPort:       rtpPort,
		AutoApprove:   autoApprove,
	}, os.Stdin, stdout)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "phonecam start failed: %v\n", err)
		return 1
	}
	return 0
}

// parseStartFlags parses the flags for `phonecam start`. It returns the
// resolved ports and auto-approve setting; ok is false when the caller should
// exit with the returned code (2 on an unknown/invalid flag, 0 on -h/--help).
//
// Pairing is auto-approved by default: the QR token already proves the phone
// saw this machine's screen, so a second confirmation adds friction without
// adding trust. --require-approval restores the interactive y/N prompt.
func parseStartFlags(args []string, stderr io.Writer) (controlPort, rtpPort int, autoApprove bool, code int, ok bool) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: phonecam start [flags]

Start pairing and the Linux receiver.

Flags:
  --control-port int    TCP control port (0 = ephemeral/random) (default 47470)
  --rtp-port int        UDP RTP port (0 = ephemeral/random) (default 47471)
  --require-approval    Ask y/N before accepting a phone (default: auto-approve
                        the phone that scanned the QR)
`)
	}
	control := fs.Int("control-port", start.DefaultControlPort, "TCP control port (0 = ephemeral/random)")
	rtp := fs.Int("rtp-port", start.DefaultRTPPort, "UDP RTP port (0 = ephemeral/random)")
	requireApproval := fs.Bool("require-approval", false, "ask y/N before accepting a phone")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, 0, false, 0, false
		}
		return 0, 0, false, 2, false
	}
	return *control, *rtp, !*requireApproval, 0, true
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `PhoneCam

Usage:
  phonecam <command>

Commands:
  start      Start pairing and the Linux receiver
             (flags: --control-port [47470], --rtp-port [47471]; 0 = random;
              --require-approval to confirm each phone with a y/N prompt)
  smoke      Run a local RTP loopback self-test
  status     Show whether PhoneCam is running and its pairing state
  stop       Stop the running PhoneCam receiver
  doctor     Check Linux dependencies and setup
  install    Print distro dependency hints
  version    Print version
  help       Show this help
`)
}

func printInstall(sys doctor.System, w io.Writer) {
	family := doctor.DistroFamily(sys)
	fmt.Fprintln(w, "PhoneCam install hints")
	fmt.Fprintln(w)

	switch family {
	case "arch":
		printArchInstall(w)
	case "fedora":
		printFedoraInstall(w)
	case "debian":
		printDebianInstall(w)
	default:
		fmt.Fprintf(w, "Could not select exact distro instructions for %s.\n\n", family)
		printArchInstall(w)
		printFedoraInstall(w)
		printDebianInstall(w)
	}

	fmt.Fprint(w, `
After installing v4l2loopback, load a PhoneCam-compatible virtual camera:
  sudo modprobe v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1

Make the virtual camera load automatically on every boot:
  echo v4l2loopback | sudo tee /etc/modules-load.d/v4l2loopback.conf
  echo "options v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1" | sudo tee /etc/modprobe.d/v4l2loopback.conf

Allow PhoneCam through your firewall (fixed default ports 47470/tcp, 47471/udp):
  ufw:       sudo ufw allow 47470/tcp && sudo ufw allow 47471/udp
  firewalld: sudo firewall-cmd --permanent --add-port=47470/tcp --add-port=47471/udp && sudo firewall-cmd --reload
`)
}

func printArchInstall(w io.Writer) {
	fmt.Fprint(w, `Arch:
  sudo pacman -S gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-plugins-ugly gst-libav v4l2loopback-dkms linux-headers

`)
}

func printFedoraInstall(w io.Writer) {
	fmt.Fprint(w, `Fedora:
  sudo dnf install gstreamer1 gstreamer1-plugins-base gstreamer1-plugins-good gstreamer1-plugins-bad-free gstreamer1-plugin-libav v4l2loopback

`)
}

func printDebianInstall(w io.Writer) {
	fmt.Fprint(w, `Ubuntu/Debian:
  sudo apt install gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-libav v4l2loopback-dkms linux-headers-$(uname -r)

`)
}

func findExecutable(name string) (string, error) {
	if strings.ContainsRune(name, rune(os.PathSeparator)) {
		if isExecutable(name) {
			return name, nil
		}
		return "", os.ErrNotExist
	}

	searchPath := os.Getenv("PATH")
	for _, extra := range []string{"/usr/sbin", "/sbin"} {
		if !strings.Contains(searchPath, extra) {
			searchPath += string(os.PathListSeparator) + extra
		}
	}

	for _, dir := range filepath.SplitList(searchPath) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}
