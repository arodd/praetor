package gui

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/cyber-godzilla/praetor/internal/client"
)

// PlayError is one script validation failure, for the frontend.
type PlayError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// PlayPreview describes a picked script before it is performed. A non-empty
// Errors means the script cannot be played.
type PlayPreview struct {
	Path    string      `json:"path"`
	Name    string      `json:"name"`
	Steps   int         `json:"steps"`
	FixedMs int64       `json:"fixedMs"` // sum of fixed waits only
	HasCues bool        `json:"hasCues"` // has %wait-for or %wait-key: runtime is unbounded
	Errors  []PlayError `json:"errors"`
}

// playSession is one performance. Owned by its driver goroutine except for the
// control channels, which the /pause, /resume, /stop, /next entry points use.
type playSession struct {
	steps  []client.PlayStep
	cancel chan struct{} // closed by /stop and Alt+X
	pause  chan struct{} // signalled by /pause
	resume chan struct{} // signalled by /resume
	next   chan struct{} // signalled by /next
	paused bool          // guarded by playMu
}

// PickPlayFile opens the picker, parses the chosen script, and returns a
// preview. A cancelled picker returns the zero PlayPreview and a nil error.
func (a *GuiApp) PickPlayFile() (PlayPreview, error) {
	if a.deps.Dialogs == nil {
		return PlayPreview{}, nil
	}
	path, err := a.deps.Dialogs.PickFile("Select a script to play", "")
	if err != nil || path == "" {
		return PlayPreview{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PlayPreview{}, err
	}
	steps, perrs := client.ParsePlayScript(string(data))

	p := PlayPreview{Path: path, Name: filepath.Base(path), Steps: len(steps)}
	for _, e := range perrs {
		p.Errors = append(p.Errors, PlayError{Line: e.Line, Message: e.Msg})
	}
	for _, s := range steps {
		switch s.Kind {
		case client.StepWait:
			p.FixedMs += s.Dur.Milliseconds()
		case client.StepWaitRandom:
			p.FixedMs += s.Dur.Milliseconds() // lower bound; the estimate is labelled approximate
		case client.StepWaitFor, client.StepWaitKey:
			p.HasCues = true
		}
	}
	return p, nil
}

// StartPlay parses path and begins performing it. It refuses an invalid script
// outright: validation exists so faults surface before anything is sent.
func (a *GuiApp) StartPlay(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	steps, perrs := client.ParsePlayScript(string(data))
	if len(perrs) > 0 {
		return fmt.Errorf("%s has %d error(s); first: %s", filepath.Base(path), len(perrs), perrs[0].Error())
	}
	if len(steps) == 0 {
		return fmt.Errorf("%s has nothing to play", filepath.Base(path))
	}

	a.playMu.Lock()
	if a.play != nil {
		a.playMu.Unlock()
		return fmt.Errorf("a performance is already running — /stop it first")
	}
	s := &playSession{
		steps:  steps,
		cancel: make(chan struct{}),
		pause:  make(chan struct{}, 1),
		resume: make(chan struct{}, 1),
		next:   make(chan struct{}, 1),
	}
	a.play = s
	a.playMu.Unlock()

	go a.runPlay(s)
	return nil
}

// PlayActive reports whether a performance is running or paused.
func (a *GuiApp) PlayActive() bool {
	a.playMu.Lock()
	defer a.playMu.Unlock()
	return a.play != nil
}

// PausePlay holds the performance. Reports whether one was running to hold.
func (a *GuiApp) PausePlay() bool {
	a.playMu.Lock()
	defer a.playMu.Unlock()
	if a.play == nil || a.play.paused {
		return false
	}
	a.play.paused = true
	select {
	case a.play.pause <- struct{}{}:
	default:
	}
	return true
}

// ResumePlay continues a held performance from the pending step.
func (a *GuiApp) ResumePlay() bool {
	a.playMu.Lock()
	defer a.playMu.Unlock()
	if a.play == nil || !a.play.paused {
		return false
	}
	a.play.paused = false
	select {
	case a.play.resume <- struct{}{}:
	default:
	}
	return true
}

// StopPlay halts the performance and drops its state.
func (a *GuiApp) StopPlay() bool {
	a.playMu.Lock()
	defer a.playMu.Unlock()
	if a.play == nil {
		return false
	}
	close(a.play.cancel)
	a.play = nil
	return true
}

// NextPlayStep releases a %wait-key hold. Ignored when nothing is holding.
func (a *GuiApp) NextPlayStep() bool {
	a.playMu.Lock()
	defer a.playMu.Unlock()
	if a.play == nil {
		return false
	}
	select {
	case a.play.next <- struct{}{}:
	default:
	}
	return true
}

// runPlay executes the script. The index advances only after a step COMPLETES,
// so pausing mid-instruction leaves it on that instruction and /resume re-runs
// it from the start — a %wait restarts, a %wait-for re-arms its matcher.
func (a *GuiApp) runPlay(s *playSession) {
	defer a.finishPlay(s)

	for idx := 0; idx < len(s.steps); {
		// Honor a pause requested before this step begins.
		if !a.waitWhilePaused(s) {
			return
		}
		done, stopped := a.runPlayStep(s, s.steps[idx])
		if stopped {
			return
		}
		if done {
			idx++
		}
		// !done means paused mid-step: loop without advancing, so the step re-runs.
	}
	a.notify("Performance complete", fmt.Sprintf("%d step(s) played.", len(s.steps)))
}

// waitWhilePaused blocks until /resume or /stop. Returns false when stopped.
func (a *GuiApp) waitWhilePaused(s *playSession) bool {
	for {
		a.playMu.Lock()
		paused := s.paused
		a.playMu.Unlock()
		if !paused {
			return true
		}
		select {
		case <-s.resume:
		case <-s.cancel:
			return false
		}
	}
}

// runPlayStep executes one step. It returns (completed, stopped): completed is
// false when a pause interrupted the step, so the caller re-runs it.
func (a *GuiApp) runPlayStep(s *playSession, step client.PlayStep) (completed, stopped bool) {
	switch step.Kind {
	case client.StepText:
		if err := a.playDispatch(step.Text); err != nil {
			a.notify("Performance failed", fmt.Sprintf("line %d: %v", step.Line, err))
			return false, true
		}
		return true, false

	case client.StepNote:
		// The spec puts %note in the OUTPUT PANE, not a toast: a cue you need
		// mid-scene must stay on screen and in scrollback, not fade after 5s.
		a.emit([]WireEvent{{Kind: KindText, Text: &TextPayload{
			Text:      step.Text,
			Segments:  []Segment{{Text: step.Text, Color: "#8a8a99", Italic: true}},
			Timestamp: unixMillis(time.Now()),
		}}})
		return true, false

	case client.StepWait:
		return a.playSleep(s, step.Dur)

	case client.StepWaitRandom:
		return a.playSleep(s, a.playRandDur(step.Dur, step.DurMax))

	case client.StepWaitKey:
		select {
		case <-s.next:
			return true, false
		case <-s.pause:
			return false, false
		case <-s.cancel:
			return false, true
		}

	default:
		// StepWaitFor is added in the next task.
		return true, false
	}
}

// playSleep waits out a duration, interruptible by pause and stop.
func (a *GuiApp) playSleep(s *playSession, d time.Duration) (completed, stopped bool) {
	select {
	case <-a.playTimer(d):
		return true, false
	case <-s.pause:
		return false, false
	case <-s.cancel:
		return false, true
	}
}

// finishPlay clears the session unless a newer one already replaced it.
func (a *GuiApp) finishPlay(s *playSession) {
	a.playMu.Lock()
	if a.play == s {
		a.play = nil
	}
	a.playMu.Unlock()
}

// --- seams -----------------------------------------------------------------

func (a *GuiApp) playDispatch(line string) error {
	if a.playSend != nil {
		return a.playSend(line)
	}
	a.client().SendCommand(line)
	return nil
}

func (a *GuiApp) playTimer(d time.Duration) <-chan time.Time {
	if a.playAfter != nil {
		return a.playAfter(d)
	}
	return time.After(d)
}

func (a *GuiApp) playRandDur(min, max time.Duration) time.Duration {
	if a.playRand != nil {
		return a.playRand(min, max)
	}
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min)))
}
