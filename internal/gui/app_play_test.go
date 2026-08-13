package gui

import (
	"os"
	"path/filepath"
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
