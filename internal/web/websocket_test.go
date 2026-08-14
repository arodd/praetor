package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	appgui "github.com/cyber-godzilla/praetor/internal/gui"
)

func TestEventWebSocketRequiresAuthenticationAndSameOrigin(t *testing.T) {
	_, handler := newTestServer(t)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/events"

	_, response, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{httpServer.URL},
	})
	if err == nil {
		t.Fatal("unauthenticated websocket unexpectedly connected")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated websocket response = %#v, err=%v", response, err)
	}

	cookie, _ := loginRequest(t, handler)
	_, response, err = websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{"http://attacker.test"},
		"Cookie": []string{cookie.String()},
	})
	if err == nil {
		t.Fatal("wrong-origin websocket unexpectedly connected")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong-origin websocket response = %#v, err=%v", response, err)
	}
}

func TestProductionShapedBurstStaysConnectedAndOrderedForTwoWebSockets(
	t *testing.T,
) {
	srv, handler := newTestServer(t)
	cookieA, _ := loginRequest(t, handler)
	cookieB, _ := loginRequest(t, handler)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/v1/events?sequence_ranges=1"
	dial := func(cookie *http.Cookie) *websocket.Conn {
		connection, response, err := websocket.DefaultDialer.Dial(
			wsURL,
			http.Header{
				"Origin": []string{httpServer.URL},
				"Cookie": []string{cookie.String()},
			},
		)
		if err != nil {
			t.Fatalf("websocket dial response=%#v: %v", response, err)
		}
		_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
		var snapshot Envelope
		if err := connection.ReadJSON(&snapshot); err != nil {
			t.Fatalf("read initial snapshot: %v", err)
		}
		if snapshot.Type != "snapshot" || snapshot.Sequence != 0 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		return connection
	}
	first := dial(cookieA)
	defer first.Close()
	second := dial(cookieB)
	defer second.Close()

	const count = 500
	for index := 0; index < count; index++ {
		srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{
			inventoryEvent(index, 128),
		})
	}

	readBurst := func(name string, connection *websocket.Conn) []string {
		t.Helper()
		sequence := uint64(0)
		lines := make([]string, 0, count)
		for sequence < count {
			var envelope Envelope
			if err := connection.ReadJSON(&envelope); err != nil {
				t.Fatalf("%s read at sequence %d: %v", name, sequence, err)
			}
			if envelope.Type != "events" ||
				envelope.FromSequence != sequence+1 ||
				envelope.ToSequence < envelope.FromSequence ||
				envelope.Sequence != envelope.ToSequence {
				t.Fatalf("%s non-contiguous envelope: %#v", name, envelope)
			}
			for _, event := range envelope.Events {
				if event.Text == nil {
					t.Fatalf("%s non-text inventory event: %#v", name, event)
				}
				lines = append(lines, event.Text.Text)
			}
			sequence = envelope.ToSequence
		}
		return lines
	}
	firstLines := readBurst("first", first)
	secondLines := readBurst("second", second)
	if len(firstLines) != count || len(secondLines) != count {
		t.Fatalf(
			"burst sizes first=%d second=%d want=%d",
			len(firstLines),
			len(secondLines),
			count,
		)
	}
	for index := 0; index < count; index++ {
		prefix := fmt.Sprintf("inventory line %04d", index)
		if !strings.HasPrefix(firstLines[index], prefix) ||
			firstLines[index] != secondLines[index] {
			t.Fatalf(
				"browser streams diverged at %d: first=%q second=%q",
				index,
				firstLines[index],
				secondLines[index],
			)
		}
	}
	diagnostics := srv.hub.Diagnostics()
	if diagnostics.ActiveSubscribers != 2 ||
		diagnostics.HardLimitEvictions != 0 ||
		diagnostics.Writes < 2 {
		t.Fatalf("post-burst diagnostics = %+v", diagnostics)
	}
}

func TestEventWebSocketSnapshotThenLiveEvents(t *testing.T) {
	srv, handler := newTestServer(t)
	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{{
		Kind: appgui.KindConn,
		Conn: &appgui.ConnPayload{State: "connected"},
	}, {
		Kind: appgui.KindText,
		Text: &appgui.TextPayload{Text: "before"},
	}})

	cookie, _ := loginRequest(t, handler)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/events"
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{httpServer.URL},
		"Cookie": []string{cookie.String()},
	})
	if err != nil {
		t.Fatalf("websocket dial response=%#v: %v", response, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var snapshot Envelope
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Type != "snapshot" || snapshot.Sequence != 1 || len(snapshot.Events) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{{
		Kind: appgui.KindText,
		Text: &appgui.TextPayload{Text: "after"},
	}})
	var live Envelope
	if err := conn.ReadJSON(&live); err != nil {
		t.Fatalf("read live event: %v", err)
	}
	if live.Type != "events" || live.Sequence != 2 || len(live.Events) != 1 || live.Events[0].Text.Text != "after" {
		t.Fatalf("live event = %#v", live)
	}
}

func TestEventWebSocketReconnectResumesWithoutScrollbackSnapshot(t *testing.T) {
	srv, handler := newTestServer(t)
	cookie, _ := loginRequest(t, handler)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	baseURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/v1/events?sequence_ranges=1"
	dial := func(url string) *websocket.Conn {
		conn, response, err := websocket.DefaultDialer.Dial(url, http.Header{
			"Origin": []string{httpServer.URL},
			"Cookie": []string{cookie.String()},
		})
		if err != nil {
			t.Fatalf("dial response=%#v: %v", response, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		return conn
	}
	read := func(conn *websocket.Conn) Envelope {
		var envelope Envelope
		if err := conn.ReadJSON(&envelope); err != nil {
			t.Fatalf("read envelope: %v", err)
		}
		return envelope
	}

	first := dial(baseURL)
	snapshot := read(first)
	if snapshot.Type != "snapshot" || snapshot.Sequence != 0 {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{inventoryEvent(1, 0)})
	if live := read(first); live.Sequence != 1 {
		t.Fatalf("first live envelope = %#v", live)
	}
	_ = first.Close()
	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{inventoryEvent(2, 0)})
	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{inventoryEvent(3, 0)})

	resumeURL := fmt.Sprintf(
		"%s&resume_server_id=%s&after_sequence=1",
		baseURL,
		snapshot.ServerID,
	)
	second := dial(resumeURL)
	defer second.Close()
	if initial := read(second); initial.Type != "resume" || initial.Sequence != 1 ||
		len(initial.Events) != 0 {
		t.Fatalf("resume handshake = %#v", initial)
	}
	sequence := uint64(1)
	var resumed []appgui.WireEvent
	for sequence < 3 {
		envelope := read(second)
		if envelope.Type != "events" || envelope.FromSequence != sequence+1 {
			t.Fatalf("resumed envelope after %d = %#v", sequence, envelope)
		}
		resumed = append(resumed, envelope.Events...)
		sequence = envelope.ToSequence
	}
	if len(resumed) != 2 || resumed[0].Text.Text != "inventory line 0002" ||
		resumed[1].Text.Text != "inventory line 0003" {
		t.Fatalf("resumed events = %#v", resumed)
	}
}

func TestTwoWebSocketClientsConvergeAcrossLateJoin(t *testing.T) {
	srv, handler := newTestServer(t)
	cookieA, _ := loginRequest(t, handler)
	cookieB, _ := loginRequest(t, handler)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/events"
	dial := func(cookie *http.Cookie) *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
			"Origin": []string{httpServer.URL},
			"Cookie": []string{cookie.String()},
		})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		return conn
	}
	read := func(conn *websocket.Conn) Envelope {
		var envelope Envelope
		if err := conn.ReadJSON(&envelope); err != nil {
			t.Fatalf("read envelope: %v", err)
		}
		return envelope
	}

	first := dial(cookieA)
	defer first.Close()
	if snapshot := read(first); snapshot.Type != "snapshot" || snapshot.Sequence != 0 {
		t.Fatalf("first snapshot = %#v", snapshot)
	}
	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{{Kind: appgui.KindConn, Conn: &appgui.ConnPayload{State: "connected"}}, {
		Kind: appgui.KindText, Text: &appgui.TextPayload{Text: "late join history"},
	}})
	if live := read(first); live.Type != "events" || live.Sequence != 1 {
		t.Fatalf("first live event = %#v", live)
	}

	second := dial(cookieB)
	defer second.Close()
	secondSnapshot := read(second)
	if secondSnapshot.Type != "snapshot" || secondSnapshot.Sequence != 1 || len(secondSnapshot.Events) != 2 {
		t.Fatalf("late-join snapshot = %#v", secondSnapshot)
	}

	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{{
		Kind: appgui.KindText, Text: &appgui.TextPayload{Text: "shared live event"},
	}})
	for name, conn := range map[string]*websocket.Conn{"first": first, "second": second} {
		if live := read(conn); live.Type != "events" || live.Sequence != 2 || live.Events[0].Text.Text != "shared live event" {
			t.Fatalf("%s converged event = %#v", name, live)
		}
	}
}

func TestLogoutStopsSubsequentWebSocketEvents(t *testing.T) {
	srv, handler := newTestServer(t)
	cookie, csrf := loginRequest(t, handler)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{httpServer.URL},
		"Cookie": []string{cookie.String()},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var snapshot Envelope
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	logout := httptest.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/auth/logout", strings.NewReader(`{}`))
	logout.Header.Set("Origin", httpServer.URL)
	logout.Header.Set("X-Praetor-CSRF", csrf)
	logout.Header.Set("Content-Type", "application/json")
	logout.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, logout)
	if response.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", response.Code, response.Body.String())
	}

	srv.hub.Emit(appgui.EventChannel, []appgui.WireEvent{{
		Kind: appgui.KindText,
		Text: &appgui.TextPayload{Text: "must not reach logged-out browser"},
	}})
	var live Envelope
	if err := conn.ReadJSON(&live); err == nil {
		t.Fatalf("logged-out browser received event: %#v", live)
	}
}
