package gstreamer

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultPayloadType = 96
	DefaultClockRate   = 90000
	DefaultWidth       = 1280
	DefaultHeight      = 720
	DefaultFPS         = 30
	// DefaultJitterLatency (ms) must absorb real Wi-Fi jitter: at 20ms the
	// buffer dropped late packets so often that the stream froze every few
	// seconds until the next keyframe. 150ms is imperceptible in meetings.
	DefaultJitterLatency = 150
	DefaultDevice        = "/dev/video10"
)

type Config struct {
	RTPPort       int
	Device        string
	Width         int
	Height        int
	FPS           int
	PayloadType   int
	ClockRate     int
	JitterLatency int
}

func (c Config) normalized() Config {
	if c.Device == "" {
		c.Device = DefaultDevice
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
	if c.ClockRate == 0 {
		c.ClockRate = DefaultClockRate
	}
	if c.JitterLatency == 0 {
		c.JitterLatency = DefaultJitterLatency
	}
	return c
}

func (c Config) Validate() error {
	c = c.normalized()
	if !validPort(c.RTPPort) {
		return fmt.Errorf("invalid RTP port %d", c.RTPPort)
	}
	if !validDevice(c.Device) {
		return fmt.Errorf("invalid v4l2 device %q", c.Device)
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
	if c.ClockRate <= 0 {
		return fmt.Errorf("invalid RTP clock rate %d", c.ClockRate)
	}
	if c.JitterLatency < 0 || c.JitterLatency > 1000 {
		return fmt.Errorf("invalid jitter latency %d", c.JitterLatency)
	}
	return nil
}

func PipelineArgs(config Config) ([]string, error) {
	config = config.normalized()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	rtpCaps := fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=%d",
		config.PayloadType,
		config.ClockRate,
	)
	// Framerate is intentionally not pinned: phone MediaCodec H.264 streams often
	// carry no VUI timing info, so the decoder reports an unknown framerate and a
	// framerate-pinned capsfilter fails negotiation ("not-negotiated").
	rawCaps := fmt.Sprintf(
		"video/x-raw,format=YUY2,width=%d,height=%d",
		config.Width,
		config.Height,
	)

	return []string{
		"-q",
		"udpsrc",
		"address=127.0.0.1",
		"port=" + strconv.Itoa(config.RTPPort),
		// Keyframes arrive as bursts of hundreds of RTP packets; the kernel's
		// default UDP receive buffer (~212KB) overflows and drops fragments,
		// which freezes the stream for a whole GOP. 4MB absorbs the burst.
		"buffer-size=4194304",
		"caps=" + rtpCaps,
		"!",
		"rtpjitterbuffer",
		"latency=" + strconv.Itoa(config.JitterLatency),
		"drop-on-latency=true",
		"!",
		"rtph264depay",
		"!",
		"h264parse",
		// Repeat SPS/PPS so a mid-GOP joiner can start without the next IDR.
		"config-interval=-1",
		"!",
		"avdec_h264",
		"!",
		"videoconvert",
		"!",
		rawCaps,
		"!",
		"v4l2sink",
		"device=" + config.Device,
		"sync=false",
	}, nil
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func validDevice(device string) bool {
	device = strings.TrimSpace(device)
	if !strings.HasPrefix(device, "/dev/video") {
		return false
	}
	suffix := strings.TrimPrefix(device, "/dev/video")
	if suffix == "" {
		return false
	}
	_, err := strconv.Atoi(suffix)
	return err == nil
}
