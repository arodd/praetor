package client

import (
	"fmt"
	"strings"
	"time"
)

// StepKind identifies what one parsed script line does.
type StepKind int

const (
	// StepText is a line of game text, sent verbatim. Blank lines are StepText
	// with empty Text: a blank line terminates an in-game writing prompt, so it
	// must reach the server.
	StepText StepKind = iota
	// StepWait pauses for Dur.
	StepWait
	// StepWaitRandom pauses for a uniform random duration in [Dur, DurMax].
	StepWaitRandom
	// StepWaitFor blocks until game text matches Text. Dur, when non-zero,
	// overrides the configured default timeout.
	StepWaitFor
	// StepWaitKey holds until the performer sends /next.
	StepWaitKey
	// StepNote prints Text locally. Never sent to the game.
	StepNote
)

// PlayStep is one executable step of a parsed script.
type PlayStep struct {
	Kind   StepKind
	Line   int    // 1-based source line, for error messages and progress
	Text   string // StepText: the line; StepWaitFor: the pattern; StepNote: the note
	Dur    time.Duration
	DurMax time.Duration // StepWaitRandom upper bound
}

// ParseError is one validation failure, tied to its source line.
type ParseError struct {
	Line int
	Msg  string
}

func (e ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// instructionSigil marks an instruction line. It is deliberately NOT '@':
// SkotOS uses '@' for its own commands (@mail, @request, @macro, @clientpref),
// so lines starting '@' must pass through as ordinary game text.
const instructionSigil = '%'

// commentSigil marks a line that is never sent and never displayed.
const commentSigil = '#'

// ParsePlayScript parses a script into executable steps, collecting EVERY
// validation error rather than stopping at the first — the author fixes the
// script in one pass instead of discovering faults one performance at a time.
// Steps are returned even when errors exist; callers must refuse to play unless
// the error slice is empty.
func ParsePlayScript(text string) ([]PlayStep, []ParseError) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, nil
	}

	var steps []PlayStep
	var errs []ParseError

	for i, raw := range strings.Split(text, "\n") {
		line := i + 1
		if raw != "" && raw[0] == commentSigil {
			continue
		}
		if raw == "" || raw[0] != instructionSigil {
			steps = append(steps, PlayStep{Kind: StepText, Line: line, Text: raw})
			continue
		}
		step, err := parseInstruction(raw[1:], line)
		if err != nil {
			errs = append(errs, *err)
			continue
		}
		steps = append(steps, step)
	}
	return steps, errs
}

// parseInstruction parses one instruction line with the sigil already stripped.
func parseInstruction(body string, line int) (PlayStep, *ParseError) {
	name, arg, _ := strings.Cut(body, ":")
	name = strings.ToLower(strings.TrimSpace(name))
	fail := func(format string, a ...any) (PlayStep, *ParseError) {
		return PlayStep{}, &ParseError{Line: line, Msg: fmt.Sprintf(format, a...)}
	}

	switch name {
	case "wait":
		d, err := parsePositiveDuration(arg)
		if err != nil {
			return fail("%%wait: %v", err)
		}
		return PlayStep{Kind: StepWait, Line: line, Dur: d}, nil

	case "wait-random":
		lo, hi, ok := strings.Cut(arg, "-")
		if !ok {
			return fail("%%wait-random needs a range like 3s-7s, got %q", arg)
		}
		min, err := parsePositiveDuration(lo)
		if err != nil {
			return fail("%%wait-random lower bound: %v", err)
		}
		max, err := parsePositiveDuration(hi)
		if err != nil {
			return fail("%%wait-random upper bound: %v", err)
		}
		if min >= max {
			return fail("%%wait-random range %s-%s is inverted or empty", min, max)
		}
		return PlayStep{Kind: StepWaitRandom, Line: line, Dur: min, DurMax: max}, nil

	case "wait-for":
		pattern, timeout := splitWaitForTimeout(arg)
		if strings.TrimSpace(pattern) == "" {
			return fail("%%wait-for needs a pattern to wait for")
		}
		return PlayStep{Kind: StepWaitFor, Line: line, Text: pattern, Dur: timeout}, nil

	case "wait-key":
		if strings.TrimSpace(arg) != "" {
			return fail("%%wait-key takes no argument, got %q", arg)
		}
		return PlayStep{Kind: StepWaitKey, Line: line}, nil

	case "note":
		// The rest of the line is verbatim, colons included.
		return PlayStep{Kind: StepNote, Line: line, Text: arg}, nil

	default:
		return fail("unknown instruction %%%s", name)
	}
}

// splitWaitForTimeout separates an optional trailing timeout from a wait-for
// pattern. The final colon-delimited field is only taken as a timeout when it
// actually parses as a positive duration, so a pattern containing colons
// ("He says: run!") needs no escaping. The one thing this cannot express is a
// pattern genuinely ending in something duration-shaped; matching is substring
// based, so the author shortens the pattern instead.
func splitWaitForTimeout(arg string) (pattern string, timeout time.Duration) {
	idx := strings.LastIndex(arg, ":")
	if idx < 0 {
		return arg, 0
	}
	if d, err := parsePositiveDuration(arg[idx+1:]); err == nil {
		return arg[:idx], d
	}
	return arg, 0
}

// parsePositiveDuration accepts Go duration syntax ("500ms", "5s", "2m") and
// rejects anything zero or negative — a wait that does not wait is a script bug,
// not a no-op worth honoring silently.
func parsePositiveDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("missing duration")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (try 5s, 500ms, 2m)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %s must be greater than zero", d)
	}
	return d, nil
}
