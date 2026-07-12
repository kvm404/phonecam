package gstreamer

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DefaultEncoder is the H.264 encoder the test sender uses when none is set.
const DefaultEncoder = "x264enc"

// DefaultReadbackFrames is the number of buffers the readback pipeline reads
// before it considers the stream proven and exits.
const DefaultReadbackFrames = 30

// DefaultSenderHost is the loopback host the test sender streams to.
const DefaultSenderHost = "127.0.0.1"

// SenderConfig describes the local videotestsrc -> H.264 -> RTP -> udpsink
// pipeline used by the smoke self-test to feed the receiver.
type SenderConfig struct {
	Host        string
	RTPPort     int
	Width       int
	Height      int
	FPS         int
	PayloadType int
	Encoder     string
}

func (c SenderConfig) normalized() SenderConfig {
	if c.Host == "" {
		c.Host = DefaultSenderHost
	}
	if c.Width == 0 {
		c.Width = DefaultWidth
	}
	if c.Height == 0 {
		c.Height = DefaultHeight
	}
	if c.FPS == 0 {
		c.FPS = DefaultFPS
	}
	if c.PayloadType == 0 {
		c.PayloadType = DefaultPayloadType
	}
	if c.Encoder == "" {
		c.Encoder = DefaultEncoder
	}
	return c
}

func (c SenderConfig) Validate() error {
	c = c.normalized()
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("invalid sender host %q", c.Host)
	}
	if !validPort(c.RTPPort) {
		return fmt.Errorf("invalid RTP port %d", c.RTPPort)
	}
	if c.Width <= 0 || c.Height <= 0 {
		return fmt.Errorf("invalid dimensions %dx%d", c.Width, c.Height)
	}
	if c.FPS <= 0 || c.FPS > 120 {
		return fmt.Errorf("invalid FPS %d", c.FPS)
	}
	if c.PayloadType < 0 || c.PayloadType > 127 {
		return fmt.Errorf("invalid RTP payload type %d", c.PayloadType)
	}
	if strings.TrimSpace(c.Encoder) == "" {
		return fmt.Errorf("invalid encoder %q", c.Encoder)
	}
	return nil
}

// SenderArgs builds the gst-launch-1.0 argv for the test sender pipeline. Only
// x264enc receives low-latency tuning properties; other encoders are used with
// their defaults.
func SenderArgs(config SenderConfig) ([]string, error) {
	config = config.normalized()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	rawCaps := fmt.Sprintf(
		"video/x-raw,width=%d,height=%d,framerate=%d/1,format=I420",
		config.Width,
		config.Height,
		config.FPS,
	)

	args := []string{
		"-q",
		"videotestsrc",
		"is-live=true",
		"pattern=smpte",
		"!",
		rawCaps,
		"!",
		"videoconvert",
		"!",
		config.Encoder,
	}

	if config.Encoder == "x264enc" {
		args = append(args,
			"tune=zerolatency",
			"speed-preset=ultrafast",
			"bitrate=4000",
			"key-int-max="+strconv.Itoa(config.FPS),
		)
	}

	args = append(args,
		"!",
		"h264parse",
		"!",
		"rtph264pay",
		"pt="+strconv.Itoa(config.PayloadType),
		"config-interval=1",
		"!",
		"udpsink",
		"host="+config.Host,
		"port="+strconv.Itoa(config.RTPPort),
		"sync=false",
	)

	return args, nil
}

// ReadbackConfig describes the v4l2src -> fakesink pipeline used to prove that
// real frames are reaching the virtual camera device.
type ReadbackConfig struct {
	Device string
	Frames int
}

func (c ReadbackConfig) normalized() ReadbackConfig {
	if c.Device == "" {
		c.Device = DefaultDevice
	}
	if c.Frames == 0 {
		c.Frames = DefaultReadbackFrames
	}
	return c
}

func (c ReadbackConfig) Validate() error {
	c = c.normalized()
	if !validDevice(c.Device) {
		return fmt.Errorf("invalid v4l2 device %q", c.Device)
	}
	if c.Frames <= 0 {
		return fmt.Errorf("invalid readback frame count %d", c.Frames)
	}
	return nil
}

// ReadbackArgs builds the gst-launch-1.0 argv for the readback pipeline. It
// exits successfully only after num-buffers real frames have been captured.
func ReadbackArgs(config ReadbackConfig) ([]string, error) {
	config = config.normalized()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return []string{
		"-q",
		"v4l2src",
		"device=" + config.Device,
		"num-buffers=" + strconv.Itoa(config.Frames),
		"!",
		"fakesink",
		"sync=false",
	}, nil
}

// HasElement reports whether a GStreamer element is available by shelling out to
// gst-inspect-1.0. A nil runCommand defaults to exec.Command, so it can be
// injected in tests.
func HasElement(runCommand func(name string, args ...string) error, element string) bool {
	if runCommand == nil {
		runCommand = func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		}
	}
	return runCommand("gst-inspect-1.0", element) == nil
}
