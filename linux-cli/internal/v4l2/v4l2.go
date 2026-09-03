// Package v4l2 verifies that the PhoneCam virtual camera device is present and
// usable before the receiver pipeline is started, so failures surface as
// actionable messages instead of cryptic GStreamer errors.
package v4l2

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ExpectedCardLabel is the card_label the PhoneCam v4l2loopback device is
// created with.
const ExpectedCardLabel = "PhoneCam"

// Sysfs parameter files for a loaded v4l2loopback module. exclusive_caps is a
// per-device CSV aligned with video_nr, not a global boolean.
const (
	VideoNrParameterPath       = "/sys/module/v4l2loopback/parameters/video_nr"
	ExclusiveCapsParameterPath = "/sys/module/v4l2loopback/parameters/exclusive_caps"
)

const writeAccess = 2

// System abstracts the OS-level access needed to inspect a v4l2 device so it
// can be faked in tests.
type System interface {
	Exists(path string) bool
	CanWrite(path string) bool
	ReadFile(path string) ([]byte, error)
}

// OSSystem implements System against the real operating system.
type OSSystem struct{}

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

// Verify checks that device is a well-formed /dev/video<N> path that exists, is
// labeled as the PhoneCam virtual camera, and is writable by the current user.
// A nil sys defaults to OSSystem{}.
func Verify(sys System, device string) error {
	if sys == nil {
		sys = OSSystem{}
	}

	number, ok := deviceNumber(device)
	if !ok {
		return fmt.Errorf("invalid v4l2 device %q; expected a path like /dev/video10", device)
	}

	if !sys.Exists(device) {
		if device == "/dev/video10" {
			return fmt.Errorf("virtual camera %s does not exist; run: sudo phonecam setup", device)
		}
		return fmt.Errorf(
			"virtual camera %s does not exist; load it with: sudo modprobe v4l2loopback video_nr=%s card_label=%s exclusive_caps=1",
			device, number, ExpectedCardLabel,
		)
	}

	if data, err := sys.ReadFile("/sys/class/video4linux/video" + number + "/name"); err == nil {
		name := strings.TrimSpace(string(data))
		if name != ExpectedCardLabel {
			return fmt.Errorf(
				"virtual camera %s belongs to another camera (named %q, expected %s); choose another video_nr or reload v4l2loopback with card_label=%s",
				device, name, ExpectedCardLabel, ExpectedCardLabel,
			)
		}
	}

	if !sys.CanWrite(device) {
		return fmt.Errorf(
			"virtual camera %s is not writable by the current user; add your user to the video group or configure udev permissions, then log out and back in",
			device,
		)
	}

	videoNr, err1 := sys.ReadFile(VideoNrParameterPath)
	caps, err2 := sys.ReadFile(ExclusiveCapsParameterPath)
	if err1 == nil && err2 == nil {
		n, _ := strconv.Atoi(number)
		enabled, found := ExclusiveCapsForDevice(string(videoNr), string(caps), n)
		if found && !enabled {
			return fmt.Errorf(
				"%s has exclusive_caps disabled; PhoneCam needs exclusive_caps on this node. Reload a single device with: sudo modprobe v4l2loopback video_nr=%s card_label=%s exclusive_caps=1. For OBS/multi-device, unload then: sudo rmmod v4l2loopback && sudo modprobe v4l2loopback devices=2 video_nr=0,%s card_label=\"OBS Virtual Camera,PhoneCam\" exclusive_caps=1,1",
				device, number, ExpectedCardLabel, number,
			)
		}
	}

	return nil
}

// ExclusiveCapsForDevice reports whether exclusive_caps is enabled for v4l2
// device number n using the video_nr and exclusive_caps sysfs parameter
// strings. found is false if n is not listed or the slices cannot be aligned.
func ExclusiveCapsForDevice(videoNr, exclusiveCaps string, n int) (enabled, found bool) {
	nrs := splitCSV(videoNr)
	caps := splitCSV(exclusiveCaps)
	if len(nrs) == 0 || len(caps) == 0 {
		return false, false
	}

	idx := -1
	for i, raw := range nrs {
		v, err := strconv.Atoi(raw)
		if err != nil || v == -1 {
			continue
		}
		if v == n {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(caps) {
		return false, false
	}
	return exclusiveCapsTruthy(caps[idx]), true
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return nil
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func exclusiveCapsTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "1", "true":
		return true
	default:
		return false
	}
}

// deviceNumber validates a /dev/video<N> path and returns the numeric suffix.
func deviceNumber(device string) (string, bool) {
	device = strings.TrimSpace(device)
	if !strings.HasPrefix(device, "/dev/video") {
		return "", false
	}
	suffix := strings.TrimPrefix(device, "/dev/video")
	if suffix == "" {
		return "", false
	}
	if _, err := strconv.Atoi(suffix); err != nil {
		return "", false
	}
	return suffix, true
}
