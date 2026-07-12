package gstreamer

import (
	"errors"
	"strings"
	"testing"
)

func TestSenderArgsBuildsX264Pipeline(t *testing.T) {
	args, err := SenderArgs(SenderConfig{RTPPort: 49322})
	if err != nil {
		t.Fatalf("SenderArgs failed: %v", err)
	}

	if args[0] != "-q" {
		t.Fatalf("expected -q first, got %q", args[0])
	}

	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"videotestsrc is-live=true pattern=smpte",
		"video/x-raw,width=1280,height=720,framerate=30/1,format=I420",
		"videoconvert",
		"x264enc tune=zerolatency speed-preset=ultrafast bitrate=4000 key-int-max=30",
		"h264parse",
		"rtph264pay pt=96 config-interval=1",
		"udpsink host=127.0.0.1 port=49322 sync=false",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in args:\n%s", expected, joined)
		}
	}
}

func TestSenderArgsOmitsX264TuningForOtherEncoders(t *testing.T) {
	args, err := SenderArgs(SenderConfig{RTPPort: 5000, Encoder: "openh264enc"})
	if err != nil {
		t.Fatalf("SenderArgs failed: %v", err)
	}

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "videoconvert ! openh264enc ! h264parse") {
		t.Fatalf("expected bare encoder without tuning props, got:\n%s", joined)
	}
	for _, unexpected := range []string{"tune=zerolatency", "speed-preset", "bitrate=4000", "key-int-max"} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("did not expect %q for non-x264 encoder, got:\n%s", unexpected, joined)
		}
	}
}

func TestSenderArgsUsesOverrides(t *testing.T) {
	args, err := SenderArgs(SenderConfig{
		Host:        "192.168.1.5",
		RTPPort:     6000,
		Width:       640,
		Height:      360,
		FPS:         15,
		PayloadType: 97,
		Encoder:     "x264enc",
	})
	if err != nil {
		t.Fatalf("SenderArgs failed: %v", err)
	}

	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"video/x-raw,width=640,height=360,framerate=15/1,format=I420",
		"key-int-max=15",
		"pt=97",
		"udpsink host=192.168.1.5 port=6000",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in args:\n%s", expected, joined)
		}
	}
}

func TestSenderArgsRejectsInvalidConfig(t *testing.T) {
	tests := []SenderConfig{
		{},
		{RTPPort: 70000},
		{RTPPort: 49322, Width: -1},
		{RTPPort: 49322, FPS: 121},
		{RTPPort: 49322, PayloadType: 128},
	}

	for _, test := range tests {
		if _, err := SenderArgs(test); err == nil {
			t.Fatalf("expected invalid config to fail: %#v", test)
		}
	}
}

func TestReadbackArgsBuildsPipeline(t *testing.T) {
	args, err := ReadbackArgs(ReadbackConfig{})
	if err != nil {
		t.Fatalf("ReadbackArgs failed: %v", err)
	}

	if args[0] != "-q" {
		t.Fatalf("expected -q first, got %q", args[0])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "v4l2src device=/dev/video10 num-buffers=30 ! fakesink sync=false") {
		t.Fatalf("unexpected readback args:\n%s", joined)
	}
}

func TestReadbackArgsUsesOverrides(t *testing.T) {
	args, err := ReadbackArgs(ReadbackConfig{Device: "/dev/video42", Frames: 10})
	if err != nil {
		t.Fatalf("ReadbackArgs failed: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "device=/dev/video42") || !strings.Contains(joined, "num-buffers=10") {
		t.Fatalf("expected overrides, got:\n%s", joined)
	}
}

func TestReadbackArgsRejectsInvalidConfig(t *testing.T) {
	for _, test := range []ReadbackConfig{
		{Device: "/tmp/video10"},
		{Device: "/dev/videoabc"},
		{Frames: -1},
	} {
		if _, err := ReadbackArgs(test); err == nil {
			t.Fatalf("expected invalid config to fail: %#v", test)
		}
	}
}

func TestRunnerRunSenderAndReadback(t *testing.T) {
	senderFactory := &recordingFactory{}
	if err := NewRunner(senderFactory.Command).RunSender(nil, SenderConfig{RTPPort: 49322}); err != nil {
		t.Fatalf("RunSender failed: %v", err)
	}
	name, args := senderFactory.Recorded()
	if name != "gst-launch-1.0" {
		t.Fatalf("expected gst-launch-1.0, got %q", name)
	}
	if !strings.Contains(strings.Join(args, " "), "videotestsrc") {
		t.Fatalf("expected sender args, got %q", strings.Join(args, " "))
	}

	readbackFactory := &recordingFactory{}
	if err := NewRunner(readbackFactory.Command).RunReadback(nil, ReadbackConfig{Device: "/dev/video10"}); err != nil {
		t.Fatalf("RunReadback failed: %v", err)
	}
	name, args = readbackFactory.Recorded()
	if !strings.Contains(strings.Join(args, " "), "v4l2src") {
		t.Fatalf("expected readback args, got %q", strings.Join(args, " "))
	}
}

func TestRunSenderWrapsFailure(t *testing.T) {
	factory := &recordingFactory{err: errProcessFailed}
	err := NewRunner(factory.Command).RunSender(nil, SenderConfig{RTPPort: 49322})
	if !errors.Is(err, errProcessFailed) {
		t.Fatalf("expected process failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "gstreamer sender failed") {
		t.Fatalf("expected sender label in error, got %v", err)
	}
}

func TestHasElement(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := func(name string, args ...string) error {
		gotName = name
		gotArgs = args
		return nil
	}
	if !HasElement(run, "x264enc") {
		t.Fatal("expected HasElement to report present")
	}
	if gotName != "gst-inspect-1.0" || len(gotArgs) != 1 || gotArgs[0] != "x264enc" {
		t.Fatalf("unexpected inspect call: %q %v", gotName, gotArgs)
	}

	missing := func(name string, args ...string) error {
		return errProcessFailed
	}
	if HasElement(missing, "x264enc") {
		t.Fatal("expected HasElement to report absent")
	}
}
