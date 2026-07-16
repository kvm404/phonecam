package qrcode

import (
	"fmt"
	"strings"

	goqrcode "github.com/skip2/go-qrcode"
)

// quietZone is the extra terminal-only padding added around the QR symbol. The
// go-qrcode Bitmap already embeds the QR-spec quiet zone (4 modules), so no
// additional padding is needed for reliable short-range terminal scanning.
const quietZone = 0

func RenderTerminal(content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("qr content is empty")
	}

	// Low error correction is sufficient for short-range, high-contrast
	// terminal scanning and yields a smaller symbol (fewer modules).
	code, err := goqrcode.New(content, goqrcode.Low)
	if err != nil {
		return "", err
	}

	bitmap := code.Bitmap()
	if len(bitmap) == 0 {
		return "", fmt.Errorf("qr bitmap is empty")
	}

	size := len(bitmap) + 2*quietZone
	rows := make([]string, 0, (size+1)/2)
	for y := 0; y < size; y += 2 {
		var row strings.Builder
		for x := 0; x < size; x++ {
			top := module(bitmap, x-quietZone, y-quietZone)
			bottom := module(bitmap, x-quietZone, y+1-quietZone)
			row.WriteRune(block(top, bottom))
		}
		rows = append(rows, row.String())
	}

	return strings.Join(rows, "\n"), nil
}

func module(bitmap [][]bool, x, y int) bool {
	if y < 0 || y >= len(bitmap) || x < 0 || x >= len(bitmap[y]) {
		return false
	}
	return bitmap[y][x]
}

func block(top, bottom bool) rune {
	switch {
	case top && bottom:
		return '█'
	case top:
		return '▀'
	case bottom:
		return '▄'
	default:
		return ' '
	}
}
