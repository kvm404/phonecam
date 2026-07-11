package qrcode

import (
	"strings"
	"testing"
)

func TestRenderTerminalReturnsBlockQRCode(t *testing.T) {
	rendered, err := RenderTerminal("phonecam-test")
	if err != nil {
		t.Fatalf("RenderTerminal failed: %v", err)
	}

	if !strings.ContainsAny(rendered, "█▀▄") {
		t.Fatalf("expected terminal block characters, got:\n%s", rendered)
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) < 10 {
		t.Fatalf("expected multiple QR lines, got %d", len(lines))
	}
	width := len([]rune(lines[0]))
	for _, line := range lines {
		if got := len([]rune(line)); got != width {
			t.Fatalf("expected equal line widths, got %d and %d", width, got)
		}
	}
}

func TestRenderTerminalRejectsEmptyContent(t *testing.T) {
	if _, err := RenderTerminal(""); err == nil {
		t.Fatal("expected empty content to fail")
	}
}
