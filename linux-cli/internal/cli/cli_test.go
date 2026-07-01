package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type fakeSystem struct {
	paths       map[string]string
	exists      map[string]bool
	writable    map[string]bool
	files       map[string]string
	environment map[string]string
}

func (f fakeSystem) LookPath(name string) (string, error) {
	if path, ok := f.paths[name]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

func (f fakeSystem) RunCommand(name string, args ...string) error {
	return nil
}

func (f fakeSystem) Exists(path string) bool {
	return f.exists[path]
}

func (f fakeSystem) CanWrite(path string) bool {
	return f.writable[path]
}

func (f fakeSystem) ReadFile(path string) ([]byte, error) {
	if content, ok := f.files[path]; ok {
		return []byte(content), nil
	}
	return nil, errors.New("not found")
}

func (f fakeSystem) Getenv(name string) string {
	return f.environment[name]
}

func TestRunShowsHelpWithoutArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "phonecam <command>") {
		t.Fatalf("expected help output, got:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr, got:\n%s", stderr.String())
	}
}

func TestRunUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"wat"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command: wat") {
		t.Fatalf("expected unknown command output, got:\n%s", stderr.String())
	}
}

func TestRunDoctorReturnsFailureCodeWhenChecksFail(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{}, []string{"doctor"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "PhoneCam Doctor") {
		t.Fatalf("expected doctor output, got:\n%s", stdout.String())
	}
}

func TestRunInstallPrioritizesDetectedDistro(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, fakeSystem{
		files: map[string]string{
			"/etc/os-release": "ID=arch\n",
		},
	}, []string{"install"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	output := stdout.String()
	if !strings.Contains(output, "gst-libav") {
		t.Fatalf("expected Arch libav package, got:\n%s", output)
	}
	if strings.Contains(output, "sudo dnf install") {
		t.Fatalf("expected only detected distro instructions, got:\n%s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr, got:\n%s", stderr.String())
	}
}
