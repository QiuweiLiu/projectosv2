// Package diff renders a bounded line diff for the cockpit. It is deliberately
// small: one artifact, text only, no rename detection and no binary payloads.
package diff

import (
	"fmt"
	"strings"
)

const (
	MaxLines = 2000
	MaxBytes = 1 << 20
)

// Line is one rendered diff line. Op is ' ', '-' or '+'.
type Line struct {
	Op   byte
	Text string
}

// Text compares two strings line by line. It returns an error only when the
// input is too large; a nil slice means the inputs are identical.
func Text(old, new string) ([]Line, error) {
	if len(old) > MaxBytes || len(new) > MaxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", MaxBytes)
	}
	a := strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n")
	b := strings.Split(strings.ReplaceAll(new, "\r\n", "\n"), "\n")
	if len(a) > MaxLines || len(b) > MaxLines {
		return nil, fmt.Errorf("file exceeds %d lines", MaxLines)
	}
	// Trim the artificial trailing empty line so a final newline does not show
	// up as a changed line.
	if len(a) > 0 && a[len(a)-1] == "" {
		a = a[:len(a)-1]
	}
	if len(b) > 0 && b[len(b)-1] == "" {
		b = b[:len(b)-1]
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []Line
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, Line{Op: ' ', Text: a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, Line{Op: '-', Text: a[i]})
			i++
		default:
			out = append(out, Line{Op: '+', Text: b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, Line{Op: '-', Text: a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, Line{Op: '+', Text: b[j]})
	}
	return out, nil
}

// Render turns lines into unified-diff-like text with a small context window.
// The cockpit colors by the first byte, so this stays machine-simple.
func Render(lines []Line) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteByte(line.Op)
		b.WriteString(line.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Changed reports whether the line set contains an insertion or deletion.
func Changed(lines []Line) bool {
	for _, line := range lines {
		if line.Op != ' ' {
			return true
		}
	}
	return false
}
