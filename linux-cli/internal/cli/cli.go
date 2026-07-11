package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kvm404/phonecam/linux-cli/internal/doctor"
	"github.com/kvm404/phonecam/linux-cli/internal/start"
)

const version = "0.0.0-dev"
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
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := start.New(start.OSSystem{}).Run(runCtx, start.Config{VirtualCamera: start.DefaultVirtualCamera}, stdout)
		if err != nil && err != context.Canceled {
			fmt.Fprintf(stderr, "phonecam start failed: %v\n", err)
			return 1
		}
		return 0
	case "status":
		fmt.Fprintln(stderr, "phonecam status is not implemented yet.")
		return 2
	case "stop":
		fmt.Fprintln(stderr, "phonecam stop is not implemented yet.")
		return 2
	case "install":
		printInstall(sys, stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return 2
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `PhoneCam

Usage:
  phonecam <command>

Commands:
  start      Start pairing and the Linux receiver
  status     Show receiver and stream status
  stop       Stop the receiver
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
