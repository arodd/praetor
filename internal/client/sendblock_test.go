package client

import (
	"strings"
	"testing"
)

func TestSplitSendBatches(t *testing.T) {
	line := func(n int) string { return strings.TrimSuffix(strings.Repeat("word\n", n), "\n") }

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"lone blank line is not content", "\n", nil},
		{"single line", "look", []string{"look"}},
		{"crlf normalized", "one\r\ntwo", []string{"one\ntwo"}},
		{"trailing newline ignored", "one\ntwo\n", []string{"one\ntwo"}},
		{"blank line ends its batch", "a\n\nb", []string{"a\n", "b"}},
		{"whitespace-only line counts as blank", "a\n   \nb", []string{"a\n   ", "b"}},
		{"exactly 50 lines stays one batch", line(50), []string{line(50)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitSendBatches(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d batches %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("batch %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitSendBatches_51LinesChunksBy20(t *testing.T) {
	in := strings.TrimSuffix(strings.Repeat("word\n", 51), "\n")
	got := SplitSendBatches(in)
	if len(got) != 3 {
		t.Fatalf("got %d batches, want 3", len(got))
	}
	for i, want := range []int{20, 20, 11} {
		if n := len(strings.Split(got[i], "\n")); n != want {
			t.Errorf("batch %d has %d lines, want %d", i, n, want)
		}
	}
}

// 500 clean lines is the headline case: 25 batches of exactly 20.
func TestSplitSendBatches_500Lines(t *testing.T) {
	in := strings.TrimSuffix(strings.Repeat("word\n", 500), "\n")
	got := SplitSendBatches(in)
	if len(got) != 25 {
		t.Fatalf("got %d batches, want 25", len(got))
	}
	for i, b := range got {
		if n := len(strings.Split(b, "\n")); n != 20 {
			t.Errorf("batch %d has %d lines, want 20", i, n)
		}
	}
}

// 500 lines with blank lines interleaved: both rules must hold at once —
// every blank line ends a batch, and no batch exceeds 20 lines.
func TestSplitSendBatches_500LinesWithBlanks(t *testing.T) {
	var sb strings.Builder
	total := 0
	for i := 0; i < 500; i++ {
		if i > 0 && i%37 == 0 {
			sb.WriteString("\n") // a completely empty line
			total++
		}
		sb.WriteString("word\n")
		total++
	}
	got := SplitSendBatches(strings.TrimSuffix(sb.String(), "\n"))

	rejoined := 0
	for i, b := range got {
		lines := strings.Split(b, "\n")
		if len(lines) > 20 {
			t.Errorf("batch %d has %d lines, exceeds the 20-line cap", i, len(lines))
		}
		for j, l := range lines {
			if strings.TrimSpace(l) == "" && j != len(lines)-1 {
				t.Errorf("batch %d: blank line at index %d is not last — it must end its batch", i, j)
			}
		}
		rejoined += len(lines)
	}
	if rejoined != total {
		t.Errorf("batches hold %d lines total, want %d — lines were lost or duplicated", rejoined, total)
	}
}
