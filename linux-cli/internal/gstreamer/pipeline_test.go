package gstreamer

import (
	"strings"
	"testing"
)

func TestPipelineArgsBuildsV0Pipeline(t *testing.T) {
	args, err := PipelineArgs(Config{RTPPort: 49322})
	if err != nil {
		t.Fatalf("PipelineArgs failed: %v", err)
	}

	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"udpsrc",
		"port=49322",
		"application/x-rtp,media=video,encoding-name=H264,payload=96,clock-rate=90000",
		"rtpjitterbuffer",
		"latency=20",
		"drop-on-latency=true",
		"rtph264depay",
		"h264parse",
		"avdec_h264",
		"videoconvert",
		"video/x-raw,format=YUY2,width=1280,height=720,framerate=30/1",
		"v4l2sink",
		"device=/dev/video10",
		"sync=false",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in args:\n%s", expected, joined)
		}
	}
}

func TestPipelineArgsUsesOverrides(t *testing.T) {
	args, err := PipelineArgs(Config{
		RTPPort:       5000,
		Device:        "/dev/video42",
		Width:         640,
		Height:        360,
		FPS:           15,
		PayloadType:   97,
		ClockRate:     90000,
		JitterLatency: 5,
	})
	if err != nil {
		t.Fatalf("PipelineArgs failed: %v", err)
	}

	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"port=5000",
		"payload=97",
		"latency=5",
		"video/x-raw,format=YUY2,width=640,height=360,framerate=15/1",
		"device=/dev/video42",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in args:\n%s", expected, joined)
		}
	}
}

func TestPipelineArgsRejectsInvalidConfig(t *testing.T) {
	tests := []Config{
		{},
		{RTPPort: 70000},
		{RTPPort: 49322, Device: "/tmp/video10"},
		{RTPPort: 49322, Device: "/dev/videoabc"},
		{RTPPort: 49322, Width: -1},
		{RTPPort: 49322, Height: -1},
		{RTPPort: 49322, FPS: 121},
		{RTPPort: 49322, PayloadType: 128},
		{RTPPort: 49322, ClockRate: -1},
		{RTPPort: 49322, JitterLatency: 1001},
	}

	for _, test := range tests {
		if _, err := PipelineArgs(test); err == nil {
			t.Fatalf("expected invalid config to fail: %#v", test)
		}
	}
}
