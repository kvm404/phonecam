package gstreamer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

var errProcessFailed = errors.New("process failed")

type fakeCommand struct {
	err    error
	stdout string
	stderr string
}

func (f fakeCommand) Run() error {
	if f.err == nil {
		return nil
	}
	return f.err
}

func (f fakeCommand) Output() (string, string) {
	return f.stdout, f.stderr
}

type recordingFactory struct {
	mu     sync.Mutex
	name   string
	args   []string
	err    error
	stdout string
	stderr string
}

func (f *recordingFactory) Command(ctx context.Context, name string, args ...string) Command {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.name = name
	f.args = append([]string(nil), args...)
	return fakeCommand{err: f.err, stdout: f.stdout, stderr: f.stderr}
}

func (f *recordingFactory) Recorded() (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.name, append([]string(nil), f.args...)
}

func TestRunnerLaunchesGstLaunchWithPipelineArgs(t *testing.T) {
	factory := &recordingFactory{}
	err := NewRunner(factory.Command).Run(context.Background(), Config{RTPPort: 49322})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	name, args := factory.Recorded()
	if name != "gst-launch-1.0" {
		t.Fatalf("expected gst-launch-1.0, got %q", name)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "udpsrc") || !strings.Contains(joined, "v4l2sink") {
		t.Fatalf("expected pipeline args, got %q", joined)
	}
}

func TestRunnerReturnsValidationErrorBeforeLaunch(t *testing.T) {
	factory := &recordingFactory{}
	err := NewRunner(factory.Command).Run(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected invalid config to fail")
	}

	name, _ := factory.Recorded()
	if name != "" {
		t.Fatalf("expected no process launch, got %q", name)
	}
}

func TestRunnerWrapsProcessFailure(t *testing.T) {
	factory := &recordingFactory{err: errProcessFailed}
	err := NewRunner(factory.Command).Run(context.Background(), Config{RTPPort: 49322})
	if !errors.Is(err, errProcessFailed) {
		t.Fatalf("expected process failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "gstreamer receiver failed") {
		t.Fatalf("expected context in error, got %v", err)
	}
}

func TestRunnerPassesContextToCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	factory := func(ctx context.Context, name string, args ...string) Command {
		return blockingCommand{ctx: ctx}
	}

	done := make(chan error, 1)
	go func() {
		done <- NewRunner(factory).Run(ctx, Config{RTPPort: 49322})
	}()

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestExecCommandIncludesCapturedOutputInError(t *testing.T) {
	err := commandError(errProcessFailed, "stdout detail", "stderr detail")
	if !strings.Contains(err.Error(), "stderr detail") || !strings.Contains(err.Error(), "stdout detail") {
		t.Fatalf("expected stderr and stdout details, got %v", err)
	}
}

func TestRunnerLastOutputAfterFailedRun(t *testing.T) {
	factory := &recordingFactory{
		err:    errProcessFailed,
		stdout: "pipeline notice",
		stderr: "gst-launch-1.0: syntax error",
	}
	runner := NewRunner(factory.Command)
	err := runner.Run(context.Background(), Config{RTPPort: 49322})
	if !errors.Is(err, errProcessFailed) {
		t.Fatalf("expected process failure, got %v", err)
	}

	stdout, stderr := runner.LastOutput()
	if stdout != "pipeline notice" {
		t.Fatalf("LastOutput stdout = %q, want pipeline notice", stdout)
	}
	if stderr != "gst-launch-1.0: syntax error" {
		t.Fatalf("LastOutput stderr = %q, want gst-launch stderr", stderr)
	}
}

func TestRunnerLastOutputEmptyWithoutCommandOutput(t *testing.T) {
	factory := func(ctx context.Context, name string, args ...string) Command {
		return runOnlyCommand{err: errProcessFailed}
	}
	runner := NewRunner(factory)
	if err := runner.Run(context.Background(), Config{RTPPort: 49322}); err == nil {
		t.Fatal("expected process failure")
	}
	stdout, stderr := runner.LastOutput()
	if stdout != "" || stderr != "" {
		t.Fatalf("expected empty LastOutput, got (%q, %q)", stdout, stderr)
	}
}

type runOnlyCommand struct {
	err error
}

func (c runOnlyCommand) Run() error {
	return c.err
}

type blockingCommand struct {
	ctx context.Context
}

func (b blockingCommand) Run() error {
	<-b.ctx.Done()
	return b.ctx.Err()
}
