package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cyber-godzilla/praetor/internal/types"
	"github.com/gorilla/websocket"
)

// newVerbatimRecordingServer is like newRecordingServer, but it does NOT
// strings.TrimRight the received frame. newRecordingServer (and the gui
// package's newSendRoutingServer) both trim "\r\n" off every frame before
// recording it, which makes them structurally unable to distinguish "a" from
// "a\n" — exactly the bug class this test guards against, so a trimming
// recorder cannot be used to catch it.
func newVerbatimRecordingServer(t *testing.T) (*httptest.Server, string, <-chan string) {
	t.Helper()
	received := make(chan string, 16)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			text := string(msg)
			if strings.HasPrefix(text, "SKOTOS ") {
				continue // ident frame, not a game command
			}
			received <- text
		}
	}))
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http"), received
}

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

// Orchil sends a multi-line block as a single message with embedded newlines
// (orchil.js:1086). Splitting it per line would change what the server sees.
func TestSendBlock_SendsOneMessageWithEmbeddedNewlines(t *testing.T) {
	srv, wsURL, recv := newRecordingServer(t)
	defer srv.Close()

	c := newDiscTestClient(t)
	defer c.Engine.Close()
	connectTestSession(t, c, wsURL)

	if err := c.SendBlock("first line\r\nsecond line\r\nthird line"); err != nil {
		t.Fatalf("SendBlock: %v", err)
	}

	select {
	case got := <-recv:
		want := "first line\nsecond line\nthird line"
		if got != want {
			t.Fatalf("server received %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the block")
	}

	select {
	case extra := <-recv:
		t.Fatalf("block was split — server got a second message %q", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSendBlock_TrailingBlankLineTerminatorIsTransmitted is the critical
// regression test for the final-review finding: SplitSendBatches deliberately
// ends a batch ON its blank line and keeps that blank as the batch's last
// line, because a blank line is what terminates a writing prompt in-game. If
// SendBlock strips that trailing newline (as it used to, via TrimSuffix), the
// terminator never reaches the server and the in-game prompt stays open,
// swallowing the next batch's first line.
//
// This must use a verbatim (untrimmed) recorder: both newRecordingServer here
// and newSendRoutingServer in internal/gui apply strings.TrimRight(msg,
// "\r\n") to the received frame, which makes "a" and "a\n" indistinguishable
// on arrival — exactly the distinction this bug turns on.
func TestSendBlock_TrailingBlankLineTerminatorIsTransmitted(t *testing.T) {
	srv, wsURL, recv := newVerbatimRecordingServer(t)
	defer srv.Close()

	c := newDiscTestClient(t)
	defer c.Engine.Close()
	connectTestSession(t, c, wsURL)

	// This is exactly what SplitSendBatches produces for the batch ending a
	// blank-line boundary: the blank line kept as the batch's last line.
	batch := "first line\nsecond line\n"
	if err := c.SendBlock(batch); err != nil {
		t.Fatalf("SendBlock: %v", err)
	}

	select {
	case got := <-recv:
		// session.Send always appends "\r\n" on top of whatever SendBlock hands
		// it, so the wire form is the block plus that fixed suffix. The blank
		// line survives as an embedded "\n" right before the final "\r\n".
		want := "first line\nsecond line\n\r\n"
		if got != want {
			t.Fatalf("server received %q, want %q — the blank-line batch terminator was stripped", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the block")
	}
}

// TestSendBlock_NoSpuriousBlankLineForOrdinaryBlock is the companion check:
// an ordinary block with no deliberate trailing blank line must not gain one.
// SendBlock no longer trims a trailing newline itself, so this only holds
// because SplitSendBatches (and callers building blocks by hand) don't leave
// one unless they mean it.
func TestSendBlock_NoSpuriousBlankLineForOrdinaryBlock(t *testing.T) {
	srv, wsURL, recv := newVerbatimRecordingServer(t)
	defer srv.Close()

	c := newDiscTestClient(t)
	defer c.Engine.Close()
	connectTestSession(t, c, wsURL)

	if err := c.SendBlock("first line\nsecond line"); err != nil {
		t.Fatalf("SendBlock: %v", err)
	}

	select {
	case got := <-recv:
		want := "first line\nsecond line\r\n"
		if got != want {
			t.Fatalf("server received %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the block")
	}
}

func TestSendBlock_EchoesEachLine(t *testing.T) {
	srv, wsURL, _ := newRecordingServer(t)
	defer srv.Close()

	c := newDiscTestClient(t)
	defer c.Engine.Close()
	connectTestSession(t, c, wsURL)
	c.Settings.EchoTyped = true

	if err := c.SendBlock("alpha\nbeta"); err != nil {
		t.Fatalf("SendBlock: %v", err)
	}

	var echoed []string
	for len(echoed) < 2 {
		select {
		case ev := <-c.events:
			if tev, ok := ev.(types.GameTextEvent); ok && tev.IsEcho {
				echoed = append(echoed, tev.Styled[0].Text)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("got echoes %v, want [alpha beta]", echoed)
		}
	}
	if echoed[0] != "alpha" || echoed[1] != "beta" {
		t.Fatalf("got echoes %v, want [alpha beta]", echoed)
	}
}
