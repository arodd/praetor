package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyber-godzilla/praetor/internal/session"
)

func TestCommandHistoryAuthorityBoundsAndPreservesDuplicates(t *testing.T) {
	history := newCommandHistoryAuthority()
	for i := 0; i < maxCommandHistoryEntries+5; i++ {
		text := fmt.Sprintf("command %d", i)
		if i >= maxCommandHistoryEntries+3 {
			text = "duplicate"
		}
		if _, err := history.commit(
			"browser", fmt.Sprintf("submission-%d", i), "history-only", text,
		); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := history.snapshot()
	if len(snapshot.Entries) != maxCommandHistoryEntries {
		t.Fatalf("history length=%d, want %d", len(snapshot.Entries), maxCommandHistoryEntries)
	}
	if snapshot.Entries[0].Text != "command 5" {
		t.Fatalf("oldest retained entry=%q, want command 5", snapshot.Entries[0].Text)
	}
	if got := snapshot.Entries[len(snapshot.Entries)-2:]; got[0].Text != "duplicate" || got[1].Text != "duplicate" {
		t.Fatalf("duplicate submissions were collapsed: %#v", got)
	}
}

func TestCommandHistoryAuthorityScopesIdempotencyToBrowserAndEpoch(t *testing.T) {
	history := newCommandHistoryAuthority()
	first, err := history.commit("browser-a", "request-1", "game", "look")
	if err != nil || first.History.Entry == nil {
		t.Fatalf("first commit=%#v err=%v", first, err)
	}
	retry, ok, err := history.lookup("browser-a", "request-1", "game", "look")
	if err != nil || !ok || retry.History.Entry == nil ||
		retry.History.Entry.ID != first.History.Entry.ID {
		t.Fatalf("idempotent lookup=%#v ok=%t err=%v", retry, ok, err)
	}
	if _, _, err := history.lookup(
		"browser-a", "request-1", "game", "skills",
	); !errors.Is(err, errCommandSubmissionIDReuse) {
		t.Fatalf("mismatched reuse error=%v", err)
	}
	second, err := history.commit("browser-b", "request-1", "game", "look")
	if err != nil || second.History.Entry == nil ||
		second.History.Entry.ID == first.History.Entry.ID {
		t.Fatalf("second browser commit=%#v err=%v", second, err)
	}

	beforeEpoch := history.epoch
	reset := history.reset()
	if reset.Epoch != beforeEpoch+1 || reset.Revision != 0 ||
		!reset.Replace || len(reset.Entries) != 0 {
		t.Fatalf("reset=%#v", reset)
	}
	if _, ok, err := history.lookup(
		"browser-a", "request-1", "game", "look",
	); ok || err != nil {
		t.Fatalf("prior-epoch dedupe survived: ok=%t err=%v", ok, err)
	}
}

func TestTypedCommandHistorySynchronizesBrowsersAndLateJoinSnapshot(t *testing.T) {
	srv, handler := newTestServer(t)
	browserA, csrfA := loginRequest(t, handler)
	browserB, csrfB := loginRequest(t, handler)
	srv.opMu.Lock()
	srv.conn = "connected"
	srv.opMu.Unlock()

	first, _ := srv.hub.Subscribe()
	second, _ := srv.hub.Subscribe()
	defer srv.hub.Unsubscribe(first.ID)
	defer srv.hub.Unsubscribe(second.ID)

	submit := func(
		cookie *http.Cookie, csrf, input, disposition, submissionID string,
	) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{
			"input": input, "disposition": disposition,
			"submissionId": submissionID,
		})
		req := httptest.NewRequest(
			http.MethodPost,
			"http://praetor.test/api/v1/typed-commands",
			strings.NewReader(string(body)),
		)
		req.Host = "praetor.test"
		req.Header.Set("Origin", "http://praetor.test")
		req.Header.Set("X-Praetor-CSRF", csrf)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	response := submit(browserA, csrfA, "/help", "history-only", "a-1")
	if response.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	for index, subscription := range []Subscription{first, second} {
		envelope := <-subscription.Messages
		if envelope.Type != "commandHistory" ||
			envelope.CommandHistory == nil ||
			envelope.CommandHistory.Entry == nil ||
			envelope.CommandHistory.Entry.Text != "/help" {
			t.Fatalf("browser %d history envelope=%#v", index, envelope)
		}
	}

	// An HTTP retry for one browser returns the original result without a new
	// broadcast, while another browser may use the same opaque request ID.
	response = submit(browserA, csrfA, "/help", "history-only", "a-1")
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	response = submit(browserB, csrfB, "skills", "history-only", "a-1")
	if response.Code != http.StatusAccepted {
		t.Fatalf("second browser status=%d body=%s", response.Code, response.Body.String())
	}
	<-first.Messages
	<-second.Messages

	response = submit(browserA, csrfA, "different", "history-only", "a-1")
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "submission_id_reused") {
		t.Fatalf("reused submission status=%d body=%s", response.Code, response.Body.String())
	}

	late, snapshot := srv.hub.Subscribe()
	defer srv.hub.Unsubscribe(late.ID)
	if snapshot.CommandHistory == nil || !snapshot.CommandHistory.Replace ||
		len(snapshot.CommandHistory.Entries) != 2 ||
		snapshot.CommandHistory.Entries[0].Text != "/help" ||
		snapshot.CommandHistory.Entries[1].Text != "skills" {
		t.Fatalf("late-join history=%#v", snapshot.CommandHistory)
	}
}

func TestTypedGameCommandCommitsOnce(t *testing.T) {
	srv, handler, _ := newTestServerWithCredentials(t, &session.MockCredentialStore{})
	cookie, csrf := loginRequest(t, handler)
	srv.opMu.Lock()
	srv.conn = "connected"
	srv.opMu.Unlock()

	submit := func(input, submissionID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{
			"input": input, "disposition": "game",
			"submissionId": submissionID,
		})
		req := httptest.NewRequest(
			http.MethodPost,
			"http://praetor.test/api/v1/typed-commands",
			strings.NewReader(string(body)),
		)
		req.Host = "praetor.test"
		req.Header.Set("Origin", "http://praetor.test")
		req.Header.Set("X-Praetor-CSRF", csrf)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	first := submit("look", "game-1")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResult typedCommandResult
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.History.Entry == nil ||
		firstResult.History.Entry.Text != "look" {
		t.Fatalf("first result=%#v", firstResult)
	}

	retry := submit("look", "game-1")
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	var retryResult typedCommandResult
	if err := json.Unmarshal(retry.Body.Bytes(), &retryResult); err != nil {
		t.Fatal(err)
	}
	if retryResult.History.Entry == nil ||
		retryResult.History.Entry.ID != firstResult.History.Entry.ID {
		t.Fatalf("retry result=%#v, first=%#v", retryResult, firstResult)
	}

	_, snapshot := srv.hub.Subscribe()
	if snapshot.CommandHistory == nil ||
		len(snapshot.CommandHistory.Entries) != 1 ||
		snapshot.CommandHistory.Entries[0].Text != "look" {
		t.Fatalf("history after retry=%#v", snapshot.CommandHistory)
	}
}

func TestTypedCommandRequestValidation(t *testing.T) {
	_, handler := newTestServer(t)
	cookie, csrf := loginRequest(t, handler)
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown disposition", body: `{"input":"look","disposition":"script","submissionId":"one"}`},
		{name: "missing ID", body: `{"input":"look","disposition":"game"}`},
		{name: "unsafe ID", body: `{"input":"look","disposition":"game","submissionId":"one/two"}`},
		{name: "unknown field", body: `{"input":"look","disposition":"game","submissionId":"one","epoch":4}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"http://praetor.test/api/v1/typed-commands",
				strings.NewReader(test.body),
			)
			req.Host = "praetor.test"
			req.Header.Set("Origin", "http://praetor.test")
			req.Header.Set("X-Praetor-CSRF", csrf)
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestTypedCommandHistoryExcludesBlankRejectedAndLegacySendPaths(t *testing.T) {
	srv, handler := newTestServer(t)
	cookie, csrf := loginRequest(t, handler)

	submitTyped := func(input, disposition, id string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(
			`{"input":%q,"disposition":%q,"submissionId":%q}`,
			input, disposition, id,
		)
		req := httptest.NewRequest(
			http.MethodPost, "http://praetor.test/api/v1/typed-commands",
			strings.NewReader(body),
		)
		req.Host = "praetor.test"
		req.Header.Set("Origin", "http://praetor.test")
		req.Header.Set("X-Praetor-CSRF", csrf)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	if rr := submitTyped("rejected", "game", "reject-1"); rr.Code != http.StatusConflict {
		t.Fatalf("disconnected typed command status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := submitTyped("/help", "history-only", "local-1"); rr.Code != http.StatusAccepted {
		t.Fatalf("disconnected local command status=%d body=%s", rr.Code, rr.Body.String())
	}
	srv.opMu.Lock()
	srv.conn = "connected"
	srv.opMu.Unlock()
	if rr := submitTyped("   ", "history-only", "blank-1"); rr.Code != http.StatusAccepted {
		t.Fatalf("blank typed command status=%d body=%s", rr.Code, rr.Body.String())
	}

	legacy := httptest.NewRequest(
		http.MethodPost, "http://praetor.test/api/v1/commands",
		strings.NewReader(`{"input":"action button"}`),
	)
	legacy.Host = "praetor.test"
	legacy.Header.Set("Origin", "http://praetor.test")
	legacy.Header.Set("X-Praetor-CSRF", csrf)
	legacy.Header.Set("Content-Type", "application/json")
	legacy.AddCookie(cookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, legacy)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("legacy command status=%d body=%s", rr.Code, rr.Body.String())
	}

	_, snapshot := srv.hub.Subscribe()
	if snapshot.CommandHistory == nil || len(snapshot.CommandHistory.Entries) != 1 ||
		snapshot.CommandHistory.Entries[0].Text != "/help" {
		t.Fatalf("excluded submissions entered history: %#v", snapshot.CommandHistory)
	}
}
