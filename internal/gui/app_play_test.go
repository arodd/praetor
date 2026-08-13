package gui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyber-godzilla/praetor/internal/client"
	"github.com/cyber-godzilla/praetor/internal/engine"
	"github.com/cyber-godzilla/praetor/internal/session"
	"github.com/gorilla/websocket"
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
	// Tests in this package build GuiApp via newTestApp, which leaves deps.Client
	// nil, so the real pre-flight would refuse every performance as disconnected.
	// Default the seam to "pass" here; tests that specifically want to exercise
	// the pre-flight override it themselves.
	a.playCheck = func() error { return nil }
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

var playTestUpgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// newConnectedTestSession spins up a local WebSocket server and returns a real
// session.Session already Connect()-ed to it. This exists so playPreflight's
// !IsConnected() branch can be exercised through the ACTUAL Session type
// (session.New() alone would already report disconnected, which only covers
// the nil/never-dialed case, not "was dialed and IsConnected() says so").
func newConnectedTestSession(t *testing.T) *session.Session {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := playTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Keep the connection open (reading and discarding client frames, e.g.
		// the ident line and pings) until the test tears it down.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	s := session.New()
	t.Cleanup(s.Close)
	if err := s.Connect(wsURL, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return s
}

// The nil-client test above only exercises playPreflight's outermost
// nil-safety short-circuit. These three tests drive the REAL function (no
// playCheck stub) through its two substantive refusal branches plus the
// idle-mode pass-through, so a future refactor that inverts or drops either
// condition breaks a test instead of shipping silently.

func TestPlayPreflight_DisconnectedSessionRefuses(t *testing.T) {
	a := newTestApp(t)
	// A real, never-Connect()-ed Session: IsConnected() is false for it, the
	// same state production sees before login.
	a.deps.Client = &client.Client{Session: session.New()}

	err := a.playPreflight()
	if err == nil {
		t.Fatal("playPreflight returned nil for a disconnected session — want an error")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %q, want it to mention being disconnected (so a reword can't silently swap this with the mode refusal)", err.Error())
	}
}

func TestPlayPreflight_ActiveModeRefusesAndNamesIt(t *testing.T) {
	sess := newConnectedTestSession(t)

	eng, err := engine.NewEngine(nil, nil, "")
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(eng.Close)
	eng.SetMode("attack", nil)

	a := newTestApp(t)
	a.deps.Client = &client.Client{Session: sess, Engine: eng}

	err = a.playPreflight()
	if err == nil {
		t.Fatal("playPreflight returned nil while a non-idle mode is running — want an error")
	}
	if !strings.Contains(err.Error(), "attack") {
		t.Errorf("error = %q, want it to name the active mode %q", err.Error(), "attack")
	}
}

func TestPlayPreflight_IdleModesAreAccepted(t *testing.T) {
	for _, idle := range []string{"", "disable"} {
		t.Run(fmt.Sprintf("mode=%q", idle), func(t *testing.T) {
			sess := newConnectedTestSession(t)

			eng, err := engine.NewEngine(nil, nil, "")
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			t.Cleanup(eng.Close)
			if idle != "" {
				eng.SetMode(idle, nil)
			}
			if got := eng.CurrentMode(); got != idle {
				t.Fatalf("CurrentMode() = %q, want %q", got, idle)
			}

			a := newTestApp(t)
			a.deps.Client = &client.Client{Session: sess, Engine: eng}

			if err := a.playPreflight(); err != nil {
				t.Errorf("playPreflight() = %v, want nil for idle mode %q", err, idle)
			}
		})
	}
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
	a.playCheck = func() error { return nil }
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
	a.playCheck = func() error { return nil }
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
	a.playCheck = func() error { return nil }
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
	a.playCheck = func() error { return nil }
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

func TestWaitFor_ProceedsOnMatchingText(t *testing.T) {
	a := newTestApp(t)
	a.playCheck = func() error { return nil }
	sent := make(chan string, 8)
	a.playSend = func(s string) error { sent <- s; return nil }
	// playTestApp's default playAfter fires instantly, which is correct for
	// %wait/%wait-random (those tests want no real sleeping) but wrong here:
	// it would time out this %wait-for before the cue could ever arrive, since
	// the mock ignores the requested duration entirely. Use a deadline that
	// never fires so only the cue (fed below) can complete the step.
	a.playAfter = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	a.playRand = func(min, max time.Duration) time.Duration { return min }
	path := writeScript(t, "%wait-for:Aldric draws\nafter the cue\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	select {
	case s := <-sent:
		t.Fatalf("sent %q before the cue arrived", s)
	case <-time.After(150 * time.Millisecond):
	}

	// Case differs from the pattern on purpose: matching is case-insensitive.
	a.feedPlayText("ALDRIC DRAWS his blade.")

	select {
	case got := <-sent:
		if got != "after the cue" {
			t.Fatalf("sent %q, want %q", got, "after the cue")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not continue after the cue")
	}
}

func TestWaitFor_TimeoutHaltsButKeepsState(t *testing.T) {
	a, sent := playTestApp(t)
	// playAfter fires immediately, so the cue wait times out at once.
	path := writeScript(t, "%wait-for:never happens\nnever sent\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	select {
	case s := <-sent:
		t.Fatalf("sent %q after a timed-out cue — playback must halt", s)
	case <-time.After(300 * time.Millisecond):
	}

	// State is KEPT so a late cue costs a /resume, not the whole scene.
	if !a.PlayActive() {
		t.Fatal("PlayActive is false after a %wait-for timeout — state must be kept")
	}
	if !a.ResumePlay() {
		t.Error("ResumePlay failed after a %wait-for timeout")
	}
}

func TestStartPlay_RefusesWhenPreflightFails(t *testing.T) {
	a, _ := playTestApp(t)
	a.playCheck = func() error { return fmt.Errorf("not connected — nothing was played") }

	if err := a.StartPlay(writeScript(t, "hello\n")); err == nil {
		t.Fatal("StartPlay succeeded despite a failing pre-flight check")
	}
	if a.PlayActive() {
		t.Error("PlayActive is true after a refused start")
	}
}

// The real pre-flight must be nil-safe: newTestApp leaves deps.Client nil, and a
// panic here would take down the whole event loop in production too.
func TestPlayPreflight_NilClientIsTreatedAsDisconnected(t *testing.T) {
	a := newTestApp(t)
	err := a.playPreflight()
	if err == nil {
		t.Fatal("playPreflight returned nil with no client — want a disconnected error")
	}
}

func TestStartPlay_ProceedsWhenPreflightPasses(t *testing.T) {
	a, sent := playTestApp(t)
	a.playCheck = func() error { return nil }

	if err := a.StartPlay(writeScript(t, "hello\n")); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}
	select {
	case got := <-sent:
		if got != "hello" {
			t.Fatalf("sent %q, want %q", got, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was sent after a passing pre-flight")
	}
}

// TestStartPlay_SlashLineReachesGameNotLocalCommand is the fix-round-1
// regression test: a play-script line beginning "/" is ordinary StepText to
// the parser (it has no % sigil), and must reach the game VERBATIM, exactly
// like a line starting with "@". Before this fix, playDispatch's default path
// called client.SendCommand, which intercepts any leading "/" as a local
// command — "/mode aggro" fired a real mode switch mid-performance instead of
// reaching the game, and any other "/"-prefixed line was silently swallowed.
// This uses the real client/session/engine (via newSendRoutingApp, the same
// recording-WebSocket harness /send's routing tests use) specifically because
// the bug lived in real dispatch plumbing that playTestApp's playSend stub
// bypasses entirely.
func TestStartPlay_SlashLineReachesGameNotLocalCommand(t *testing.T) {
	a, recv := newSendRoutingApp(t)

	path := writeScript(t, "/mode aggro\n")
	if err := a.StartPlay(path); err != nil {
		t.Fatalf("StartPlay: %v", err)
	}

	select {
	case msg := <-recv:
		if msg != "/mode aggro" {
			t.Fatalf("server received %q, want %q — script line must reach the game verbatim", msg, "/mode aggro")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the script line — /mode must not be handled locally by a play script")
	}

	if got := a.CurrentMode(); got == "aggro" {
		t.Fatalf("CurrentMode() = %q — a play-script line must never fire a client command like /mode", got)
	}
}
