package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPickSendFile_CountsLinesAndBatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scene.txt")
	body := strings.TrimSuffix(strings.Repeat("word\n", 60), "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(t)
	a.deps.Dialogs = &fakeDialogs{file: path}

	got, err := a.PickSendFile()
	if err != nil {
		t.Fatalf("PickSendFile: %v", err)
	}
	if got.Lines != 60 {
		t.Errorf("Lines = %d, want 60", got.Lines)
	}
	if got.Batches != 3 { // 60 > 50, so chunked: 20 + 20 + 20
		t.Errorf("Batches = %d, want 3", got.Batches)
	}
	if got.Name != "scene.txt" {
		t.Errorf("Name = %q, want scene.txt", got.Name)
	}
}

func TestPickSendFile_CancelledPickerReturnsZero(t *testing.T) {
	a := newTestApp(t)
	a.deps.Dialogs = &fakeDialogs{file: ""}

	got, err := a.PickSendFile()
	if err != nil {
		t.Fatalf("PickSendFile: %v", err)
	}
	if got.Path != "" || got.Lines != 0 {
		t.Errorf("cancelled picker returned %+v, want zero value", got)
	}
}

func TestAbortSend_StopsRemainingBatches(t *testing.T) {
	a := newTestApp(t)

	// 200 lines => 10 batches, 250ms apart: far longer than this test waits.
	batches := make([]string, 10)
	for i := range batches {
		batches[i] = "line"
	}
	sent := make(chan int, len(batches))
	a.sendOne = func(b string) error { sent <- 1; return nil }

	a.startSend(batches)
	time.Sleep(50 * time.Millisecond) // first batch goes immediately

	if !a.AbortSend() {
		t.Fatal("AbortSend returned false while a send was in flight")
	}
	time.Sleep(600 * time.Millisecond) // long enough for 2 more batch delays

	if n := len(sent); n != 1 {
		t.Fatalf("%d batches sent after abort, want 1", n)
	}
	if a.AbortSend() {
		t.Error("AbortSend returned true with no send in flight")
	}
}
