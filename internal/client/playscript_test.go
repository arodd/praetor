package client

import (
	"testing"
	"time"
)

func TestParsePlayScript_Steps(t *testing.T) {
	in := "" +
		"# a stage direction\n" +
		"emote steps forward.\n" +
		"%wait:5s\n" +
		"say Hello there.\n" +
		"\n" +
		"@mail Aldric\n" +
		"/not a command\n" +
		"%wait-random:3s-7s\n" +
		"%wait-for:Aldric draws\n" +
		"%wait-for:Aldric draws:45s\n" +
		"%wait-key\n" +
		"%note:he should be drawing now\n"

	steps, errs := ParsePlayScript(in)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	want := []PlayStep{
		{Kind: StepText, Line: 2, Text: "emote steps forward."},
		{Kind: StepWait, Line: 3, Dur: 5 * time.Second},
		{Kind: StepText, Line: 4, Text: "say Hello there."},
		{Kind: StepText, Line: 5, Text: ""},
		{Kind: StepText, Line: 6, Text: "@mail Aldric"},
		{Kind: StepText, Line: 7, Text: "/not a command"},
		{Kind: StepWaitRandom, Line: 8, Dur: 3 * time.Second, DurMax: 7 * time.Second},
		{Kind: StepWaitFor, Line: 9, Text: "Aldric draws"},
		{Kind: StepWaitFor, Line: 10, Text: "Aldric draws", Dur: 45 * time.Second},
		{Kind: StepWaitKey, Line: 11},
		{Kind: StepNote, Line: 12, Text: "he should be drawing now"},
	}
	if len(steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}

// A pattern containing colons needs no escaping: the last field is only taken
// as a timeout when it actually parses as a duration.
func TestParsePlayScript_ColonsInPattern(t *testing.T) {
	steps, errs := ParsePlayScript("%wait-for:He says: run!\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if steps[0].Text != "He says: run!" || steps[0].Dur != 0 {
		t.Fatalf("got %+v, want pattern %q with no timeout", steps[0], "He says: run!")
	}
}

// A trailing field that PARSES as a duration is always a timeout, so a
// non-positive one is a script error rather than silently becoming part of
// the pattern (which would produce an unmatchable pattern and a quiet hang).
func TestParsePlayScript_WaitForNonPositiveTimeout(t *testing.T) {
	for _, in := range []string{"%wait-for:x:0s\n", "%wait-for:x:-5s\n"} {
		_, errs := ParsePlayScript(in)
		if len(errs) != 1 {
			t.Fatalf("input %q: got %d errors, want 1: %v", in, len(errs), errs)
		}
		if errs[0].Line != 1 {
			t.Errorf("input %q: error line = %d, want 1", in, errs[0].Line)
		}
		if errs[0].Msg == "" {
			t.Errorf("input %q: error message is empty", in)
		}
	}
}

// Guard against over-correcting TestParsePlayScript_WaitForNonPositiveTimeout:
// a trailing field that does NOT parse as a duration is still ordinary
// pattern text, and a trailing field that parses as a POSITIVE duration is
// still accepted as a timeout.
func TestParsePlayScript_WaitForTimeoutStillWorks(t *testing.T) {
	steps, errs := ParsePlayScript("%wait-for:He says: run!\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if steps[0].Text != "He says: run!" || steps[0].Dur != 0 {
		t.Fatalf("got %+v, want pattern %q with no timeout", steps[0], "He says: run!")
	}

	steps, errs = ParsePlayScript("%wait-for:the bell tolls:5s\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if steps[0].Text != "the bell tolls" || steps[0].Dur != 5*time.Second {
		t.Fatalf("got %+v, want pattern %q with Dur 5s", steps[0], "the bell tolls")
	}
}

func TestParsePlayScript_NoteKeepsColons(t *testing.T) {
	steps, _ := ParsePlayScript("%note:cue: he draws\n")
	if steps[0].Text != "cue: he draws" {
		t.Fatalf("note text = %q, want %q", steps[0].Text, "cue: he draws")
	}
}

func TestParsePlayScript_CRLF(t *testing.T) {
	steps, errs := ParsePlayScript("say hi\r\n%wait:1s\r\n")
	if len(errs) != 0 || len(steps) != 2 {
		t.Fatalf("got %d steps, %v errors", len(steps), errs)
	}
	if steps[0].Text != "say hi" {
		t.Errorf("text = %q, want %q (stray CR)", steps[0].Text, "say hi")
	}
}

// Every error is reported, with its line number — not just the first.
func TestParsePlayScript_ReportsAllErrors(t *testing.T) {
	in := "" +
		"%bogus:1s\n" +
		"%wait:abc\n" +
		"%wait:0s\n" +
		"%wait:-5s\n" +
		"%wait-random:7s-3s\n" +
		"%wait-for:\n" +
		"%wait-key:extra\n"

	_, errs := ParsePlayScript(in)
	if len(errs) != 7 {
		t.Fatalf("got %d errors, want 7: %v", len(errs), errs)
	}
	for i, e := range errs {
		if e.Line != i+1 {
			t.Errorf("error %d has line %d, want %d (%s)", i, e.Line, i+1, e.Msg)
		}
		if e.Msg == "" {
			t.Errorf("error on line %d has an empty message", e.Line)
		}
	}
}

func TestParsePlayScript_NoSendableContent(t *testing.T) {
	for _, in := range []string{"", "# just a comment\n", "\n", "\n\n"} {
		steps, errs := ParsePlayScript(in)
		if in == "\n\n" {
			// Blank lines ARE sendable — they terminate in-game prompts.
			if len(steps) != 2 || len(errs) != 0 {
				t.Errorf("blank-line script: got %d steps, %v errors", len(steps), errs)
			}
			continue
		}
		// A single trailing newline is treated as empty input, not as one blank
		// line to send — matching the ruling for /send in SplitSendBatches.
		if len(steps) != 0 || len(errs) != 0 {
			t.Errorf("input %q produced %d steps, %d errors, want 0, 0", in, len(steps), len(errs))
		}
	}
}
