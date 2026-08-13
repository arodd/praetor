package gui

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// playTestApp returns an app whose sends are captured and whose waits complete
// immediately, so playback order is asserted without sleeping.
func playTestApp(t *testing.T) (*GuiApp, chan string) {
	t.Helper()
	a := newTestApp(t)
	sent := make(chan string, 64)
	a.playSend = func(s string) error { sent <- s; return nil }
	a.playAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	a.playRand = func(min, max time.Duration) time.Duration { return min }
	return a, sent
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scene.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartPlay_SendsTextInOrderAndSkipsNotesAndComments(t *testing.T) {
	a, sent := playTestApp(t)
	path := writeScript(t, "# comment\nfirst\n%note:local only\n%wait:5s\nsecond\n")

	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	for _, want := range []string{"first", "second"} {
		select {
		case got := <-sent:
			if got != want {
				t.Fatalf("sent %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	select {
	case extra := <-sent:
		t.Fatalf("sent extra line %q — comments and notes must not be sent", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPausePlay_HoldsAndResumeReRunsTheInstruction(t *testing.T) {
	a := newTestApp(t)
	sent := make(chan string, 8)
	a.playSend = func(s string) error { sent <- s; return nil }
	a.playRand = func(min, max time.Duration) time.Duration { return min }

	// A wait long enough that the test controls when it would finish.
	waits := make(chan chan time.Time, 4)
	a.playAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		waits <- ch
		return ch
	}

	path := writeScript(t, "%wait:30s\nafter the wait\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	<-waits // the first wait is now pending
	if !a.PausePlay() {
		t.Fatal("PausePlay returned false while playing")
	}
	select {
	case s := <-sent:
		t.Fatalf("sent %q while paused", s)
	case <-time.After(100 * time.Millisecond):
	}

	if !a.ResumePlay() {
		t.Fatal("ResumePlay returned false while paused")
	}
	// Resume must re-run the interrupted %wait, i.e. request a NEW timer.
	select {
	case ch := <-waits:
		ch <- time.Time{} // let the re-run wait elapse
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not re-run the interrupted %wait")
	}

	select {
	case got := <-sent:
		if got != "after the wait" {
			t.Fatalf("sent %q, want %q", got, "after the wait")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not continue after resume")
	}
}

func TestStopPlay_DropsStateAndHalts(t *testing.T) {
	a, sent := playTestApp(t)
	waits := make(chan chan time.Time, 4)
	a.playAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		waits <- ch
		return ch
	}
	path := writeScript(t, "%wait:30s\nnever sent\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}
	<-waits

	if !a.StopPlay() {
		t.Fatal("StopPlay returned false while playing")
	}
	if a.PlayActive() {
		t.Error("PlayActive is true after StopPlay")
	}
	if a.ResumePlay() {
		t.Error("ResumePlay succeeded after StopPlay — state must be dropped")
	}
	select {
	case s := <-sent:
		t.Fatalf("sent %q after stop", s)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWaitKey_HoldsUntilNext(t *testing.T) {
	a, sent := playTestApp(t)
	path := writeScript(t, "%wait-key\nafter the key\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	select {
	case s := <-sent:
		t.Fatalf("sent %q before /next", s)
	case <-time.After(150 * time.Millisecond):
	}

	if !a.NextPlayStep() {
		t.Fatal("NextPlayStep returned false while holding on %wait-key")
	}
	select {
	case got := <-sent:
		if got != "after the key" {
			t.Fatalf("sent %q, want %q", got, "after the key")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not continue after /next")
	}
}

func TestStartPlay_RefusesInvalidScript(t *testing.T) {
	a, _ := playTestApp(t)
	path := writeScript(t, "%bogus:1s\ngood line\n")
	if err := a.StartPlay(path); err == nil {
		t.Fatal("StartPlay accepted a script with a validation error")
	}
	if a.PlayActive() {
		t.Error("PlayActive is true after refusing an invalid script")
	}
}

// TestStopPlay_HaltsBetweenConsecutiveTextSteps is a regression test for a
// defect where /stop was only observed inside a blocking step (playSleep or
// %wait-key's select). A run of plain text/%note lines with no waits between
// them never touched s.cancel at all, so StopPlay could return true and
// PlayActive could report false while the driver kept right on sending.
func TestStopPlay_HaltsBetweenConsecutiveTextSteps(t *testing.T) {
	a := newTestApp(t)
	sent := make(chan string, 8)
	proceed := make(chan struct{})
	a.playSend = func(s string) error {
		sent <- s
		<-proceed // hold this send "in flight" until the test releases it
		return nil
	}

	// Five plain text lines, no wait steps between them.
	path := writeScript(t, "one\ntwo\nthree\nfour\nfive\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	// Wait for the first line to be in flight (dispatch started, not yet returned).
	select {
	case got := <-sent:
		if got != "one" {
			t.Fatalf("sent %q, want %q", got, "one")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first line")
	}

	// Stop while the first line is still in flight.
	if !a.StopPlay() {
		t.Fatal("StopPlay returned false while playing")
	}
	close(proceed) // let the in-flight send (and only it) return

	// The in-flight line was already committed and may complete, but no
	// further step may begin after /stop is observed.
	select {
	case extra := <-sent:
		t.Fatalf("sent %q after StopPlay — /stop must be observed before the next step begins", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestNextPlayStep_EarlyCallDoesNotSkipWaitKey is a regression test for a
// defect where a /next arriving before any %wait-key was reached left a
// token in the buffered `next` channel, which later silently released the
// hold without the performer actually confirming it.
func TestNextPlayStep_EarlyCallDoesNotSkipWaitKey(t *testing.T) {
	a := newTestApp(t)
	sent := make(chan string, 8)
	proceed := make(chan struct{})
	a.playSend = func(s string) error {
		sent <- s
		<-proceed
		return nil
	}

	path := writeScript(t, "line one\n%wait-key\nafter the key\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	// "line one" is in flight — well before %wait-key is reached.
	select {
	case got := <-sent:
		if got != "line one" {
			t.Fatalf("sent %q, want %q", got, "line one")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first line")
	}

	// A /next this early must not be banked for the later %wait-key.
	if !a.NextPlayStep() {
		t.Fatal("NextPlayStep returned false while a performance is running")
	}
	close(proceed) // let "line one" finish and the driver reach %wait-key

	select {
	case got := <-sent:
		t.Fatalf("sent %q — an early /next incorrectly skipped %%wait-key", got)
	case <-time.After(200 * time.Millisecond):
	}

	// A /next while actually holding on %wait-key does release it.
	if !a.NextPlayStep() {
		t.Fatal("NextPlayStep returned false while holding on %wait-key")
	}
	select {
	case got := <-sent:
		if got != "after the key" {
			t.Fatalf("sent %q, want %q", got, "after the key")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not continue after /next")
	}
}

// TestPausePlay_StaleTokenDoesNotRestartALaterWait is a regression test for a
// defect where a /pause landing during a text send (a step that never
// selects on s.pause) left a token in the buffered `pause` channel. A later
// %wait step's playSleep would consume that stale token on its first select,
// report itself as "paused mid-step", and get re-run from the top — silently
// requesting a second timer for the same step.
func TestPausePlay_StaleTokenDoesNotRestartALaterWait(t *testing.T) {
	a := newTestApp(t)
	sent := make(chan string, 8)
	proceed := make(chan struct{})
	a.playSend = func(s string) error {
		sent <- s
		<-proceed
		return nil
	}
	var timerCalls int32
	a.playAfter = func(time.Duration) <-chan time.Time {
		atomic.AddInt32(&timerCalls, 1)
		return make(chan time.Time, 1) // never fed: the step should request exactly one and then hold
	}
	a.playRand = func(min, max time.Duration) time.Duration { return min }

	path := writeScript(t, "line one\n%wait:5s\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	// "line one" is in flight.
	select {
	case got := <-sent:
		if got != "line one" {
			t.Fatalf("sent %q, want %q", got, "line one")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first line")
	}

	// Pause and resume while the text step is in flight — before any step
	// selects on s.pause, so the token is left unconsumed by the pause itself.
	if !a.PausePlay() {
		t.Fatal("PausePlay returned false while playing")
	}
	if !a.ResumePlay() {
		t.Fatal("ResumePlay returned false while paused")
	}
	close(proceed) // let "line one" finish and the driver reach %wait

	// Give the driver time to reach (and, if buggy, re-enter) the %wait step.
	time.Sleep(200 * time.Millisecond)

	if n := atomic.LoadInt32(&timerCalls); n != 1 {
		t.Fatalf("playAfter called %d time(s) for the %%wait step, want 1 — a stale /pause token restarted it", n)
	}
}

func TestPickPlayFile_ReportsPreviewAndErrors(t *testing.T) {
	a := newTestApp(t)
	path := writeScript(t, "one\n%wait:5s\n%wait-key\n%bogus:x\n")
	a.deps.Dialogs = &fakeDialogs{file: path}

	got, err := a.PickPlayFile()
	if err != nil {
		t.Fatalf("PickPlayFile: %v", err)
	}
	if len(got.Errors) != 1 || got.Errors[0].Line != 4 {
		t.Fatalf("Errors = %+v, want one error on line 4", got.Errors)
	}
	if got.Name != "scene.txt" {
		t.Errorf("Name = %q, want scene.txt", got.Name)
	}
	if !got.HasCues {
		t.Error("HasCues = false, want true (%wait-key is unbounded)")
	}
	if got.FixedMs != 5000 {
		t.Errorf("FixedMs = %d, want 5000", got.FixedMs)
	}
}
