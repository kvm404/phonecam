package doctor

import (
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
)

const (
	StatusPass = "PASS"
	StatusWarn = "WARN"
	StatusFail = "FAIL"
	StatusInfo = "INFO"
)

type System interface {
	LookPath(name string) (string, error)
	RunCommand(name string, args ...string) error
	Exists(path string) bool
	CanWrite(path string) bool
	ReadFile(path string) ([]byte, error)
	Getenv(name string) string
}

type Check struct {
	Name    string
	Status  string
	Message string
	Fix     string
}

type Report struct {
	Checks []Check
}

func (r Report) HasFailures() bool {
	for _, check := range r.Checks {
		if check.Status == StatusFail {
			return true
		}
	}
	return false
}

func Run(sys System) Report {
	var checks []Check

	checks = append(checks, commandCheck(sys, "GStreamer launcher", "gst-launch-1.0", "Install GStreamer tools for your distro."))
	checks = append(checks, commandCheck(sys, "GStreamer inspector", "gst-inspect-1.0", "Install GStreamer tools and plugin packages."))
	checks = append(checks, commandCheck(sys, "Kernel module loader", "modprobe", "Install kmod or ensure /usr/sbin is available."))
	checks = append(checks, commandCheck(sys, "Kernel module metadata", "modinfo", "Install kmod or ensure /usr/sbin is available."))
	checks = append(checks, v4l2loopbackInstallCheck(sys))

	if sys.Exists("/sys/module/v4l2loopback") {
		checks = append(checks, Check{
			Name:    "v4l2loopback module",
			Status:  StatusPass,
			Message: "module is loaded",
		})
	} else {
		checks = append(checks, Check{
			Name:    "v4l2loopback module",
			Status:  StatusFail,
			Message: "module is not loaded",
			Fix:     "Load it with: sudo modprobe v4l2loopback video_nr=10 card_label=PhoneCam exclusive_caps=1",
		})
	}

	checks = append(checks, gstreamerElementChecks(sys)...)
	if sys.Exists("/dev/video10") {
		status := StatusPass
		message := "/dev/video10 exists"
		fix := ""
		if !sys.CanWrite("/dev/video10") {
			status = StatusFail
			message = "/dev/video10 exists but is not writable by the current user"
			fix = "Add your user to the video group or configure udev permissions, then log out and back in."
		}
		checks = append(checks, Check{
			Name:    "PhoneCam virtual camera",
			Status:  status,
			Message: message,
			Fix:     fix,
		})
		checks = append(checks, virtualCameraIdentityCheck(sys))
	} else {
		checks = append(checks, Check{
			Name:    "PhoneCam virtual camera",
			Status:  StatusFail,
			Message: "/dev/video10 does not exist",
			Fix:     "Load v4l2loopback with video_nr=10 card_label=PhoneCam exclusive_caps=1.",
		})
	}

	checks = append(checks, desktopSessionCheck(sys))
	checks = append(checks, distroCheck(sys))
	checks = append(checks, firewallCheck(sys))
	checks = append(checks, udpRcvbufCheck())
	checks = append(checks, appVisibilityGuidanceCheck())
	checks = append(checks, Check{
		Name:    "LAN privacy",
		Status:  StatusInfo,
		Message: "v0.1 uses local-network RTP/UDP without payload encryption in the first implementation spike",
		Fix:     "Use trusted local networks only until encrypted transport is implemented.",
	})

	return Report{Checks: checks}
}

func WriteReport(w io.Writer, report Report) {
	fmt.Fprintln(w, "PhoneCam Doctor")
	fmt.Fprintln(w)

	for _, check := range report.Checks {
		fmt.Fprintf(w, "[%s] %s: %s\n", check.Status, check.Name, check.Message)
		if check.Fix != "" {
			fmt.Fprintf(w, "      Fix: %s\n", check.Fix)
		}
	}

	fmt.Fprintln(w)
	if report.HasFailures() {
		fmt.Fprintln(w, "Result: issues found")
		return
	}
	fmt.Fprintln(w, "Result: ready")
}

func commandCheck(sys System, label, command, fix string) Check {
	path, err := sys.LookPath(command)
	if err != nil {
		return Check{
			Name:    label,
			Status:  StatusFail,
			Message: command + " was not found",
			Fix:     fix,
		}
	}
	return Check{
		Name:    label,
		Status:  StatusPass,
		Message: "found " + path,
	}
}

func v4l2loopbackInstallCheck(sys System) Check {
	if err := sys.RunCommand("modinfo", "v4l2loopback"); err != nil {
		return Check{
			Name:    "v4l2loopback install",
			Status:  StatusFail,
			Message: "v4l2loopback module metadata was not found",
			Fix:     "Install v4l2loopback for your kernel, then run doctor again.",
		}
	}
	return Check{
		Name:    "v4l2loopback install",
		Status:  StatusPass,
		Message: "v4l2loopback is installed",
	}
}

func gstreamerElementChecks(sys System) []Check {
	elements := []struct {
		name string
		fix  string
	}{
		{name: "udpsrc", fix: "Install GStreamer base plugins."},
		{name: "rtpjitterbuffer", fix: "Install GStreamer good plugins."},
		{name: "rtph264depay", fix: "Install GStreamer good plugins."},
		{name: "h264parse", fix: "Install GStreamer bad plugins."},
		{name: "avdec_h264", fix: "Install the GStreamer libav plugin."},
		{name: "videoconvert", fix: "Install GStreamer base plugins."},
		{name: "v4l2sink", fix: "Install GStreamer good plugins with V4L2 support."},
	}

	checks := make([]Check, 0, len(elements))
	for _, element := range elements {
		if err := sys.RunCommand("gst-inspect-1.0", element.name); err != nil {
			checks = append(checks, Check{
				Name:    "GStreamer element " + element.name,
				Status:  StatusFail,
				Message: element.name + " was not found",
				Fix:     element.fix,
			})
			continue
		}
		checks = append(checks, Check{
			Name:    "GStreamer element " + element.name,
			Status:  StatusPass,
			Message: element.name + " is available",
		})
	}
	return checks
}

func virtualCameraIdentityCheck(sys System) Check {
	data, err := sys.ReadFile("/sys/class/video4linux/video10/name")
	if err != nil {
		return Check{
			Name:    "PhoneCam virtual camera identity",
			Status:  StatusWarn,
			Message: "could not read /sys/class/video4linux/video10/name",
			Fix:     "Confirm /dev/video10 is the PhoneCam v4l2loopback device.",
		}
	}

	name := strings.TrimSpace(string(data))
	if name != "PhoneCam" {
		return Check{
			Name:    "PhoneCam virtual camera identity",
			Status:  StatusFail,
			Message: "/dev/video10 is named " + name + ", expected PhoneCam",
			Fix:     "Choose another video_nr or reload v4l2loopback with card_label=PhoneCam.",
		}
	}

	return Check{
		Name:    "PhoneCam virtual camera identity",
		Status:  StatusPass,
		Message: "/dev/video10 is labeled PhoneCam",
	}
}

func desktopSessionCheck(sys System) Check {
	session := sys.Getenv("XDG_SESSION_TYPE")
	desktop := sys.Getenv("XDG_CURRENT_DESKTOP")
	if session == "" && desktop == "" {
		return Check{
			Name:    "Desktop session",
			Status:  StatusWarn,
			Message: "could not detect Wayland/X11 session",
			Fix:     "Run doctor from your desktop session for better diagnostics.",
		}
	}

	details := strings.TrimSpace(session + " " + desktop)
	if strings.EqualFold(session, "wayland") {
		return Check{
			Name:    "Desktop session",
			Status:  StatusInfo,
			Message: details + " detected; v4l2loopback should still appear as a normal V4L2 camera to apps",
		}
	}
	return Check{
		Name:    "Desktop session",
		Status:  StatusInfo,
		Message: details + " detected",
	}
}

func distroCheck(sys System) Check {
	family := DistroFamily(sys)
	switch family {
	case "arch":
		return Check{Name: "Distro", Status: StatusInfo, Message: "Arch-family distro detected"}
	case "fedora":
		return Check{Name: "Distro", Status: StatusInfo, Message: "Fedora-family distro detected"}
	case "debian":
		return Check{Name: "Distro", Status: StatusInfo, Message: "Debian-family distro detected"}
	case "unknown":
		return Check{
			Name:    "Distro",
			Status:  StatusWarn,
			Message: "could not read /etc/os-release",
			Fix:     "Use the closest Arch, Fedora, or Ubuntu/Debian install instructions.",
		}
	default:
		return Check{
			Name:    "Distro",
			Status:  StatusWarn,
			Message: "unsupported or untested distro: " + family,
			Fix:     "Doctor can still validate generic GStreamer and v4l2loopback setup.",
		}
	}
}

// firewallCheck detects a common active host firewall (ufw or firewalld) and,
// if found, warns that it may block PhoneCam's fixed control (TCP 47470) and
// RTP (UDP 47471) ports, with a distro-appropriate allow command. It is
// best-effort and never fails the report.
func firewallCheck(sys System) Check {
	if sys.RunCommand("systemctl", "is-active", "--quiet", "ufw") == nil {
		return Check{
			Name:    "Firewall",
			Status:  StatusWarn,
			Message: "ufw is active and may block PhoneCam's control (TCP 47470) and RTP (UDP 47471) ports",
			Fix:     "Allow PhoneCam through: sudo ufw allow 47470/tcp && sudo ufw allow 47471/udp",
		}
	}
	if sys.RunCommand("systemctl", "is-active", "--quiet", "firewalld") == nil {
		return Check{
			Name:    "Firewall",
			Status:  StatusWarn,
			Message: "firewalld is active and may block PhoneCam's control (TCP 47470) and RTP (UDP 47471) ports",
			Fix:     "Allow PhoneCam through: sudo firewall-cmd --permanent --add-port=47470/tcp --add-port=47471/udp && sudo firewall-cmd --reload",
		}
	}
	return Check{
		Name:    "Firewall",
		Status:  StatusInfo,
		Message: "no active ufw or firewalld detected; PhoneCam needs local TCP control and UDP RTP ports reachable from the Android phone",
		Fix:     "If pairing or streaming fails, allow PhoneCam on your trusted LAN firewall profile.",
	}
}

func udpRcvbufCheck() Check {
	got, err := probeUDPRcvbuf(rtp.PublicRcvbuf)
	if err != nil {
		return Check{
			Name:    "UDP receive buffer",
			Status:  StatusWarn,
			Message: "could not probe SO_RCVBUF: " + err.Error(),
			Fix:     "PhoneCam needs a 4MB UDP receive buffer to absorb H.264 keyframe bursts.",
		}
	}
	if rcvbufEffective(got) < rtp.PublicRcvbuf {
		return Check{
			Name:    "UDP receive buffer",
			Status:  StatusWarn,
			Message: fmt.Sprintf("kernel SO_RCVBUF effective size is %d, want %d", rcvbufEffective(got), rtp.PublicRcvbuf),
			Fix:     "Raise net.core.rmem_max to at least 4194304 so keyframe bursts are not dropped.",
		}
	}
	return Check{
		Name:    "UDP receive buffer",
		Status:  StatusPass,
		Message: "SO_RCVBUF can reach 4MB",
	}
}

func rcvbufEffective(got int) int {
	return min(got, got/2)
}

func probeUDPRcvbuf(want int) (int, error) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return 0, fmt.Errorf("unexpected packet conn %T", conn)
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		return 0, err
	}
	var got int
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, want)
		if sockErr != nil {
			return
		}
		got, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil {
		return 0, err
	}
	return got, sockErr
}

func appVisibilityGuidanceCheck() Check {
	return Check{
		Name:    "Meeting app visibility",
		Status:  StatusInfo,
		Message: "Meet, Zoom, Discord, OBS, Chromium, and Firefox should see the v4l2loopback device after it is loaded with exclusive_caps=1",
		Fix:     "Restart sandboxed browsers or meeting apps after creating the virtual camera.",
	}
}

func DistroFamily(sys System) string {
	data, err := sys.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}

	id := osReleaseValue(string(data), "ID")
	like := osReleaseValue(string(data), "ID_LIKE")
	switch {
	case id == "arch" || strings.Contains(like, "arch"):
		return "arch"
	case id == "fedora" || strings.Contains(like, "fedora"):
		return "fedora"
	case id == "ubuntu" || id == "debian" || strings.Contains(like, "debian"):
		return "debian"
	default:
		if id == "" {
			return "unknown"
		}
		return id
	}
}

func osReleaseValue(data, key string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || name != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}
