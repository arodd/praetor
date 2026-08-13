package client

import "strings"

const (
	// batchThreshold is the line count above which a block is chunked. At or
	// below it the block goes out in one message, exactly like typed input.
	batchThreshold = 50
	// batchChunk is the lines per batch once chunking kicks in.
	batchChunk = 20
)

// SplitSendBatches splits text into batches for /send, each ready to hand to
// SendBlock. Rules, in order of precedence:
//
//   - A completely empty line (whitespace-only) always ends its batch, and is
//     kept as that batch's last line — a blank line is often what terminates a
//     writing prompt in-game, so it must reach the server in place.
//   - Blocks longer than batchThreshold are additionally chunked every
//     batchChunk lines. At or below the threshold the block stays whole.
//
// Input is CRLF-normalized and a single trailing line terminator is dropped, so
// a file ending "text\n" does not produce a spurious empty final batch. A file
// ending "text\n\n" keeps its deliberate trailing blank line.
func SplitSendBatches(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	chunked := len(lines) > batchThreshold

	var batches []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			batches = append(batches, strings.Join(cur, "\n"))
			cur = nil
		}
	}

	for _, ln := range lines {
		cur = append(cur, ln)
		if strings.TrimSpace(ln) == "" {
			flush()
			continue
		}
		if chunked && len(cur) >= batchChunk {
			flush()
		}
	}
	flush()
	return batches
}
