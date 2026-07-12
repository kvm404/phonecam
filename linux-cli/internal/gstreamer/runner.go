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

	command := r.command(ctx, "gst-launch-1.0", args...)
	if err := command.Run(); err != nil {
		return fmt.Errorf("gstreamer receiver failed: %w", err)
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
