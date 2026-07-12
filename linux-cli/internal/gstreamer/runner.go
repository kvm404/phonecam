package gstreamer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Command interface {
	Run() error
}

type CommandFactory func(ctx context.Context, name string, args ...string) Command

type Runner struct {
	command CommandFactory
}

func NewRunner(factory CommandFactory) Runner {
	if factory == nil {
		factory = osCommand
	}
	return Runner{command: factory}
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
	if err := command.Run(); err != nil {
		return fmt.Errorf("gstreamer %s failed: %w", label, err)
	}
	return nil
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
