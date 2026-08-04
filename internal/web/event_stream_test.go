package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	appgui "github.com/cyber-godzilla/praetor/internal/gui"
)

func receiveSubscription(t *testing.T, subscription Subscription) Envelope {
	t.Helper()
	select {
	case <-subscription.Ready:
		delivery, ok := subscription.next()
		if !ok {
			t.Fatalf(
				"subscription %d signaled readiness without a delivery (close reason %q)",
				subscription.ID,
				subscription.closeReason(),
			)
		}
		subscription.recordWrite(
			delivery.EstimatedBytes,
			0,
			nil,
		)
		return delivery.Envelope
	case <-subscription.Closed:
		t.Fatalf(
			"subscription %d closed before delivery: %s",
			subscription.ID,
			subscription.closeReason(),
		)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for subscription %d", subscription.ID)
	}
	return Envelope{}
}

func inventoryEvent(index int, padding int) appgui.WireEvent {
	text := fmt.Sprintf("inventory line %04d", index)
	if padding > 0 {
		text += " " + strings.Repeat("x", padding)
	}
	return appgui.WireEvent{
		Kind: appgui.KindText,
		Text: &appgui.TextPayload{
			Text:     text,
			Segments: []appgui.Segment{{Text: text}},
		},
	}
}

func drainEventSubscription(
	t *testing.T,
	subscription Subscription,
	startSequence uint64,
	endSequence uint64,
) []appgui.WireEvent {
	t.Helper()
	sequence := startSequence
	var events []appgui.WireEvent
	for sequence < endSequence {
		envelope := receiveSubscription(t, subscription)
		if envelope.Type != "events" {
			t.Fatalf("unexpected envelope while draining events: %#v", envelope)
		}
		if envelope.FromSequence != sequence+1 ||
			envelope.ToSequence < envelope.FromSequence ||
			envelope.Sequence != envelope.ToSequence {
			t.Fatalf(
				"non-contiguous range %d-%d after %d: %#v",
				envelope.FromSequence,
				envelope.ToSequence,
				sequence,
				envelope,
			)
		}
		events = append(events, envelope.Events...)
		sequence = envelope.ToSequence
	}
	if sequence != endSequence {
		t.Fatalf("drained through sequence %d, want %d", sequence, endSequence)
	}
	return events
}

func TestHubCoalescesFiveHundredEventBurstInOrder(t *testing.T) {
	hub := NewHub(1000)
	subscription, snapshot := hub.Subscribe()
	defer hub.Unsubscribe(subscription.ID)
	if snapshot.Sequence != 0 {
		t.Fatalf("initial snapshot sequence = %d", snapshot.Sequence)
	}

	const count = 500
	for index := 0; index < count; index++ {
		hub.Emit(appgui.EventChannel, []appgui.WireEvent{
			inventoryEvent(index, 96),
		})
	}

	diagnostics := hub.Diagnostics()
	if diagnostics.ActiveSubscribers != 1 ||
		diagnostics.HardLimitEvictions != 0 ||
		len(diagnostics.Subscribers) != 1 {
		t.Fatalf("burst diagnostics = %+v", diagnostics)
	}
	if diagnostics.Subscribers[0].HighWaterEvents != count ||
		diagnostics.Subscribers[0].CoalescedEnvelopes == 0 {
		t.Fatalf("burst was not retained/coalesced: %+v", diagnostics.Subscribers[0])
	}

	events := drainEventSubscription(t, subscription, 0, count)
	if len(events) != count {
		t.Fatalf("received %d events, want %d", len(events), count)
	}
	for index, event := range events {
		want := fmt.Sprintf("inventory line %04d", index)
		if event.Text == nil || !strings.HasPrefix(event.Text.Text, want) {
			t.Fatalf("event %d = %#v, want prefix %q", index, event, want)
		}
	}
	after := hub.Diagnostics().Subscribers[0]
	if after.QueuedEnvelopes != 0 || after.QueuedEvents != 0 ||
		after.QueuedBytes != 0 || after.InFlightEvents != 0 ||
		after.InFlightBytes != 0 || after.DeliveredEvents != count {
		t.Fatalf("backlog did not return to zero: %+v", after)
	}
	hub.mu.Lock()
	retainedBacklog := hub.clients[subscription.ID].backlog
	hub.mu.Unlock()
	if retainedBacklog != nil {
		t.Fatal("drained subscriber retained its backlog allocation")
	}
}

func TestLegacySubscriberKeepsSingleSequencesWithinBoundedBacklog(t *testing.T) {
	hub := NewHub(1000)
	subscription, _ := hub.SubscribeWithSequenceRanges(false)
	defer hub.Unsubscribe(subscription.ID)
	const count = 500
	for index := 0; index < count; index++ {
		hub.Emit(appgui.EventChannel, []appgui.WireEvent{
			inventoryEvent(index, 24),
		})
	}
	diagnostic := hub.Diagnostics().Subscribers[0]
	if diagnostic.SequenceRanges || diagnostic.CoalescedEnvelopes != 0 ||
		diagnostic.QueuedEnvelopes != count {
		t.Fatalf("legacy subscriber diagnostics = %+v", diagnostic)
	}
	events := drainEventSubscription(t, subscription, 0, count)
	if len(events) != count {
		t.Fatalf("legacy subscriber received %d events", len(events))
	}
}

func TestHubTemporaryLagAndTwoSubscribersConverge(t *testing.T) {
	hub := NewHub(2000)
	slow, _ := hub.Subscribe()
	fast, _ := hub.Subscribe()
	defer hub.Unsubscribe(slow.ID)
	defer hub.Unsubscribe(fast.ID)

	const count = 1000
	for index := 0; index < count; index++ {
		hub.Emit(appgui.EventChannel, []appgui.WireEvent{
			inventoryEvent(index, 24),
		})
	}
	if diagnostics := hub.Diagnostics(); diagnostics.ActiveSubscribers != 2 ||
		diagnostics.HardLimitEvictions != 0 {
		t.Fatalf("temporarily lagging subscribers were evicted: %+v", diagnostics)
	}

	fastEvents := drainEventSubscription(t, fast, 0, count)
	slowEvents := drainEventSubscription(t, slow, 0, count)
	if len(fastEvents) != count || len(slowEvents) != count {
		t.Fatalf("delivery sizes fast=%d slow=%d", len(fastEvents), len(slowEvents))
	}
	for index := range fastEvents {
		if fastEvents[index].Text == nil || slowEvents[index].Text == nil ||
			fastEvents[index].Text.Text != slowEvents[index].Text.Text {
			t.Fatalf("subscribers diverged at %d", index)
		}
	}
}

func TestHubHardEventLimitEvictsOnlyStalledSubscriber(t *testing.T) {
	hub := NewHub(subscriberBacklogMaxEvents + 100)
	slow, _ := hub.Subscribe()
	fast, _ := hub.Subscribe()
	defer hub.Unsubscribe(fast.ID)

	hub.Emit(appgui.EventChannel, []appgui.WireEvent{{
		Kind: appgui.KindConn,
		Conn: &appgui.ConnPayload{State: "connected"},
	}})
	receiveSubscription(t, fast)
	for index := 0; index <= subscriberBacklogMaxEvents; index++ {
		hub.Emit(appgui.EventChannel, []appgui.WireEvent{inventoryEvent(index, 0)})
		receiveSubscription(t, fast)
	}

	select {
	case <-slow.Closed:
	case <-time.After(time.Second):
		t.Fatal("stalled subscriber was not closed at the hard event bound")
	}
	if reason := slow.closeReason(); reason != eventStreamCloseSubscriberHardLimit {
		t.Fatalf("slow close reason = %q", reason)
	}
	diagnostics := hub.Diagnostics()
	if diagnostics.ActiveSubscribers != 1 || diagnostics.HardLimitEvictions != 1 {
		t.Fatalf("hard-limit diagnostics = %+v", diagnostics)
	}
	if fastDiagnostics := diagnostics.Subscribers[0]; fastDiagnostics.ID != fast.ID || fastDiagnostics.DeliveredEvents != subscriberBacklogMaxEvents+2 {
		t.Fatalf("healthy subscriber diagnostics = %+v", fastDiagnostics)
	}

	late, snapshot := hub.Subscribe()
	defer hub.Unsubscribe(late.ID)
	if snapshot.Sequence != subscriberBacklogMaxEvents+2 {
		t.Fatalf("snapshot sequence = %d", snapshot.Sequence)
	}
	if len(snapshot.Events) != subscriberBacklogMaxEvents+2 {
		t.Fatalf("snapshot events = %d, want %d", len(snapshot.Events), subscriberBacklogMaxEvents+2)
	}
	if snapshot.Events[0].Conn == nil || snapshot.Events[0].Conn.State != "connected" {
		t.Fatalf("subscriber eviction changed the game projection: %#v", snapshot.Events[0])
	}
}

func TestHubHardByteLimitEvictsStalledSubscriber(t *testing.T) {
	hub := NewHub(200)
	slow, _ := hub.Subscribe()
	for index := 0; index < 200; index++ {
		hub.Emit(appgui.EventChannel, []appgui.WireEvent{
			inventoryEvent(index, 64<<10),
		})
		select {
		case <-slow.Closed:
			if reason := slow.closeReason(); reason != eventStreamCloseSubscriberHardLimit {
				t.Fatalf("byte-limit close reason = %q", reason)
			}
			if diagnostics := hub.Diagnostics(); diagnostics.HardLimitEvictions != 1 {
				t.Fatalf("byte-limit diagnostics = %+v", diagnostics)
			}
			return
		default:
		}
	}
	t.Fatal("subscriber exceeded the hard byte limit without eviction")
}

func TestHubHardLimitIncludesEnvelopeCurrentlyBeingWritten(t *testing.T) {
	hub := NewHub(subscriberBacklogMaxEvents + 10)
	subscription, _ := hub.Subscribe()
	first := make([]appgui.WireEvent, 1024)
	for index := range first {
		first[index] = inventoryEvent(index, 0)
	}
	hub.Emit(appgui.EventChannel, first)
	select {
	case <-subscription.Ready:
	case <-time.After(time.Second):
		t.Fatal("first envelope was not ready")
	}
	delivery, ok := subscription.next()
	if !ok || delivery.EventCount != len(first) {
		t.Fatalf("in-flight delivery = %+v, ok=%t", delivery, ok)
	}

	queued := make([]appgui.WireEvent, subscriberBacklogMaxEvents-len(first))
	for index := range queued {
		queued[index] = inventoryEvent(len(first)+index, 0)
	}
	hub.Emit(appgui.EventChannel, queued)
	select {
	case <-subscription.Closed:
		t.Fatal("subscriber was evicted at, rather than above, the hard bound")
	default:
	}
	hub.Emit(appgui.EventChannel, []appgui.WireEvent{
		inventoryEvent(subscriberBacklogMaxEvents, 0),
	})
	select {
	case <-subscription.Closed:
	case <-time.After(time.Second):
		t.Fatal("in-flight envelope was omitted from hard-bound accounting")
	}
	if reason := subscription.closeReason(); reason != eventStreamCloseSubscriberHardLimit {
		t.Fatalf("close reason = %q", reason)
	}
	// Complete the simulated writer so retained in-flight accounting can be
	// released even though the subscription has already been isolated.
	subscription.recordWrite(delivery.EstimatedBytes, 0, nil)
}

func TestHubDoesNotCoalesceAcrossAuthoritativeState(t *testing.T) {
	hub := NewHub(100)
	subscription, _ := hub.Subscribe()
	defer hub.Unsubscribe(subscription.ID)

	hub.Emit(appgui.EventChannel, []appgui.WireEvent{inventoryEvent(1, 0)})
	hub.BroadcastState(Envelope{
		Type:     "config",
		Config:   json.RawMessage(`{"UI":{}}`),
		Revision: 2,
	})
	hub.BroadcastState(Envelope{
		Type:      "modes",
		ModeNames: []string{"idle"},
	})
	accounts := []string{"operator"}
	hub.BroadcastState(Envelope{
		Type:     "accounts",
		Accounts: &accounts,
	})
	hub.BroadcastState(Envelope{
		Type: "operation",
		Result: &OperationResult{
			Operation: "reload",
			OK:        true,
		},
	})
	hub.BroadcastState(Envelope{
		Type: "commandHistory",
		CommandHistory: &CommandHistoryUpdate{
			Epoch:    1,
			Revision: 1,
		},
	})
	hub.Emit(appgui.EventChannel, []appgui.WireEvent{inventoryEvent(2, 0)})

	wantTypes := []string{
		"events",
		"config",
		"modes",
		"accounts",
		"operation",
		"commandHistory",
		"events",
	}
	for index, wantType := range wantTypes {
		envelope := receiveSubscription(t, subscription)
		wantSequence := uint64(index + 1)
		if envelope.Type != wantType || envelope.Sequence != wantSequence {
			t.Fatalf(
				"envelope %d = type %q sequence %d, want %q/%d: %#v",
				index,
				envelope.Type,
				envelope.Sequence,
				wantType,
				wantSequence,
				envelope,
			)
		}
		if envelope.Type == "events" &&
			(envelope.FromSequence != wantSequence || envelope.ToSequence != wantSequence) {
			t.Fatalf("event envelope crossed authoritative state: %#v", envelope)
		}
	}
}
