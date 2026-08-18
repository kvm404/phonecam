package gstreamer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

type Command interface {
	Run() error
}

// commandOutput is optional on Command. recordOutput reads it after Run so
// LastOutput is independent of error wrapping.
type commandOutput interface {
	Output() (stdout, stderr string)
}

type CommandFactory func(ctx context.Context, name string, args ...string) Command

type lastOutput struct {
	mu     sync.Mutex
	stdout string
	stderr string
}

type Runner struct {
	command CommandFactory
	last    *lastOutput
}

func NewRunner(factory CommandFactory) Runner {
	if factory == nil {
		factory = osCommand
	}
	return Runner{command: factory, last: &lastOutput{}}
}

// LastOutput is stdout/stderr from the most recent gst-launch, including after
// a failed Run. Safe to call once Run has returned.
func (r Runner) LastOutput() (stdout, stderr string) {
	if r.last == nil {
		return "", ""
	}
	r.last.mu.Lock()
	defer r.last.mu.Unlock()
	return r.last.stdout, r.last.stderr
}

func (r Runner) Run(ctx context.Context, config Config) error {
	args, err := PipelineArgs(config)
	if err != nil {
		return err
	}
	return r.launch(ctx, "receiver", args)
}

// RunSender launches the test sender pipeline that feeds synthetic H.264 frames
// to the receiver over RTP.
func (r Runner) RunSender(ctx context.Context, config SenderConfig) error {
	args, err := SenderArgs(config)
	if err != nil {
		return err
	}
	return r.launch(ctx, "sender", args)
}

// RunReadback launches the readback pipeline that captures frames from the
// virtual camera and exits once enough buffers have arrived.
func (r Runner) RunReadback(ctx context.Context, config ReadbackConfig) error {
	args, err := ReadbackArgs(config)
	if err != nil {
		return err
	}
	return r.launch(ctx, "readback", args)
}

// launch execs gst-launch-1.0 with argv, wrapping failures with a label naming
// the pipeline that failed.
func (r Runner) launch(ctx context.Context, label string, args []string) error {
	command := r.command(ctx, "gst-launch-1.0", args...)
	err := command.Run()
	r.recordOutput(command)
	if err != nil {
		return fmt.Errorf("gstreamer %s failed: %w", label, err)
	}
	return nil
}

func (r Runner) recordOutput(command Command) {
	if r.last == nil {
		return
	}
	var stdout, stderr string
	if captured, ok := command.(commandOutput); ok {
		stdout, stderr = captured.Output()
	}
	r.last.mu.Lock()
	r.last.stdout = stdout
	r.last.stderr = stderr
	r.last.mu.Unlock()
}

type execCommand struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func osCommand(ctx context.Context, name string, args ...string) Command {
	cmd := exec.CommandContext(ctx, name, args...)
	command := &execCommand{cmd: cmd}
	cmd.Stdout = &command.stdout
	cmd.Stderr = &command.stderr
	return command
}

func (c *execCommand) Run() error {
	err := c.cmd.Run()
	if err == nil {
		return nil
	}
	return commandError(err, c.stdout.String(), c.stderr.String())
}

func (c *execCommand) Output() (string, string) {
	return c.stdout.String(), c.stderr.String()
}

func commandError(err error, stdout, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if stdout := strings.TrimSpace(stdout); stdout != "" {
		if detail != "" {
			detail += "\n"
		}
		detail += stdout
	}
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}
