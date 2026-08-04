package web

import (
	"encoding/json"
	"sort"
	"time"

	appgui "github.com/cyber-godzilla/praetor/internal/gui"
)

// The confirmed production inventory burst contained 217 text events in about
// 26ms. These bounds admit more than eighteen times that event count and well
// over one hundred times the observed encoded payload while still placing a
// hard per-browser ceiling on retained delivery work.
const (
	maxSubscribers                       = 64
	subscriberBacklogMaxEvents           = 4096
	subscriberBacklogMaxBytes            = 8 << 20
	subscriberCoalescedEnvelopeMaxEvents = 1024
	subscriberCoalescedEnvelopeMaxBytes  = 1 << 20
	eventStreamSlowWriteDiagnostic       = 100 * time.Millisecond
	eventStreamCloseSubscriberHardLimit  = "subscriber_backlog_hard_limit"
	eventStreamCloseClientUnsubscribe    = "client_unsubscribe"
	eventStreamCloseHubShutdown          = "hub_shutdown"
)

type queuedEnvelope struct {
	envelope        Envelope
	estimatedBytes  int
	eventCount      int
	sourceEnvelopes uint64
}

type eventSubscriber struct {
	id             uint64
	createdAt      time.Time
	ready          chan struct{}
	closed         chan struct{}
	sequenceRanges bool

	removed           bool
	closeReason       string
	backlog           []queuedEnvelope
	queuedBytes       int
	queuedEvents      int
	inFlight          bool
	inFlightBytes     int
	inFlightEvents    int
	inFlightEnvelopes uint64

	highWaterEnvelopes int
	highWaterEvents    int
	highWaterBytes     int

	receivedEnvelopes  uint64
	receivedEvents     uint64
	receivedBytes      uint64
	deliveredMessages  uint64
	deliveredEnvelopes uint64
	deliveredEvents    uint64
	deliveredBytes     uint64
	coalescedEnvelopes uint64
	discardedEnvelopes uint64
	discardedEvents    uint64
	discardedBytes     uint64

	writeCount        uint64
	writeErrors       uint64
	lastWriteBytes    int
	lastWriteDuration time.Duration
	maxWriteDuration  time.Duration
}

type eventStreamCounters struct {
	created            uint64
	removed            uint64
	hardLimitEvictions uint64
	coalescedEnvelopes uint64
	encodingFailures   uint64
	writes             uint64
	writeErrors        uint64
	maxWriteDuration   time.Duration
}

// EventStreamLimits reports the server-owned delivery bounds. They are
// intentionally diagnostic rather than configurable: a configuration edit
// must not permit one browser to make process memory unbounded.
type EventStreamLimits struct {
	MaxSubscribers     int `json:"maxSubscribers"`
	MaxBacklogEvents   int `json:"maxBacklogEvents"`
	MaxBacklogBytes    int `json:"maxBacklogBytes"`
	MaxCoalescedEvents int `json:"maxCoalescedEvents"`
	MaxCoalescedBytes  int `json:"maxCoalescedBytes"`
}

// EventSubscriberDiagnostics contains bounded operational counters only. It
// deliberately excludes browser identity, cookies, commands, and event data.
type EventSubscriberDiagnostics struct {
	ID                    uint64 `json:"id"`
	AgeMilliseconds       int64  `json:"ageMilliseconds"`
	CloseReason           string `json:"closeReason,omitempty"`
	SequenceRanges        bool   `json:"sequenceRanges"`
	QueuedEnvelopes       int    `json:"queuedEnvelopes"`
	QueuedEvents          int    `json:"queuedEvents"`
	QueuedBytes           int    `json:"queuedBytes"`
	InFlightEvents        int    `json:"inFlightEvents"`
	InFlightBytes         int    `json:"inFlightBytes"`
	HighWaterEnvelopes    int    `json:"highWaterEnvelopes"`
	HighWaterEvents       int    `json:"highWaterEvents"`
	HighWaterBytes        int    `json:"highWaterBytes"`
	ReceivedEnvelopes     uint64 `json:"receivedEnvelopes"`
	ReceivedEvents        uint64 `json:"receivedEvents"`
	ReceivedBytes         uint64 `json:"receivedBytes"`
	DeliveredMessages     uint64 `json:"deliveredMessages"`
	DeliveredEnvelopes    uint64 `json:"deliveredEnvelopes"`
	DeliveredEvents       uint64 `json:"deliveredEvents"`
	DeliveredBytes        uint64 `json:"deliveredBytes"`
	CoalescedEnvelopes    uint64 `json:"coalescedEnvelopes"`
	DiscardedEnvelopes    uint64 `json:"discardedEnvelopes"`
	DiscardedEvents       uint64 `json:"discardedEvents"`
	DiscardedBytes        uint64 `json:"discardedBytes"`
	WriteCount            uint64 `json:"writeCount"`
	WriteErrors           uint64 `json:"writeErrors"`
	LastWriteBytes        int    `json:"lastWriteBytes"`
	LastWriteMilliseconds int64  `json:"lastWriteMilliseconds"`
	MaxWriteMilliseconds  int64  `json:"maxWriteMilliseconds"`
}

type EventStreamDiagnostics struct {
	Limits               EventStreamLimits            `json:"limits"`
	ActiveSubscribers    int                          `json:"activeSubscribers"`
	Created              uint64                       `json:"created"`
	Removed              uint64                       `json:"removed"`
	HardLimitEvictions   uint64                       `json:"hardLimitEvictions"`
	CoalescedEnvelopes   uint64                       `json:"coalescedEnvelopes"`
	EncodingFailures     uint64                       `json:"encodingFailures"`
	Writes               uint64                       `json:"writes"`
	WriteErrors          uint64                       `json:"writeErrors"`
	MaxWriteMilliseconds int64                        `json:"maxWriteMilliseconds"`
	Subscribers          []EventSubscriberDiagnostics `json:"subscribers"`
}

// Subscription exposes level-triggered readiness instead of a fixed Go
// channel of envelopes. The hub can therefore combine adjacent queued event
// ranges before the WebSocket writer claims them.
type Subscription struct {
	ID     uint64
	Ready  <-chan struct{}
	Closed <-chan struct{}

	hub        *Hub
	subscriber *eventSubscriber
}

type subscriptionDelivery struct {
	Envelope             Envelope
	EstimatedBytes       int
	EventCount           int
	SourceEnvelopes      uint64
	QueuedEnvelopesAfter int
	QueuedEventsAfter    int
	QueuedBytesAfter     int
}

func (s Subscription) next() (subscriptionDelivery, bool) {
	if s.hub == nil || s.subscriber == nil || s.ID == 0 {
		return subscriptionDelivery{}, false
	}
	return s.hub.nextDelivery(s.subscriber)
}

func (s Subscription) closeReason() string {
	if s.hub == nil || s.subscriber == nil {
		return ""
	}
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.subscriber.closeReason
}

func (s Subscription) diagnostics() EventSubscriberDiagnostics {
	if s.hub == nil || s.subscriber == nil {
		return EventSubscriberDiagnostics{}
	}
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return subscriberDiagnostics(s.subscriber, time.Now())
}

func (s Subscription) recordWrite(bytes int, duration time.Duration, writeErr error) {
	if s.hub == nil || s.subscriber == nil {
		return
	}
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	client := s.subscriber
	client.writeCount++
	client.lastWriteBytes = bytes
	client.lastWriteDuration = duration
	if duration > client.maxWriteDuration {
		client.maxWriteDuration = duration
	}
	if writeErr != nil {
		client.writeErrors++
	}
	if client.inFlight {
		if writeErr == nil {
			client.deliveredMessages++
			client.deliveredEnvelopes += client.inFlightEnvelopes
			client.deliveredEvents += uint64(client.inFlightEvents)
			client.deliveredBytes += uint64(client.inFlightBytes)
		} else {
			client.discardedEnvelopes += client.inFlightEnvelopes
			client.discardedEvents += uint64(client.inFlightEvents)
			client.discardedBytes += uint64(client.inFlightBytes)
		}
		client.inFlight = false
		client.inFlightBytes = 0
		client.inFlightEvents = 0
		client.inFlightEnvelopes = 0
		if !client.removed && len(client.backlog) > 0 {
			signalSubscriberReady(client)
		}
	}
	s.hub.stream.writes++
	if writeErr != nil {
		s.hub.stream.writeErrors++
	}
	if duration > s.hub.stream.maxWriteDuration {
		s.hub.stream.maxWriteDuration = duration
	}
}

func eventStreamLimits() EventStreamLimits {
	return EventStreamLimits{
		MaxSubscribers:     maxSubscribers,
		MaxBacklogEvents:   subscriberBacklogMaxEvents,
		MaxBacklogBytes:    subscriberBacklogMaxBytes,
		MaxCoalescedEvents: subscriberCoalescedEnvelopeMaxEvents,
		MaxCoalescedBytes:  subscriberCoalescedEnvelopeMaxBytes,
	}
}

func subscriberDiagnostics(client *eventSubscriber, now time.Time) EventSubscriberDiagnostics {
	age := now.Sub(client.createdAt)
	if age < 0 {
		age = 0
	}
	return EventSubscriberDiagnostics{
		ID:                    client.id,
		AgeMilliseconds:       age.Milliseconds(),
		CloseReason:           client.closeReason,
		SequenceRanges:        client.sequenceRanges,
		QueuedEnvelopes:       len(client.backlog),
		QueuedEvents:          client.queuedEvents,
		QueuedBytes:           client.queuedBytes,
		InFlightEvents:        client.inFlightEvents,
		InFlightBytes:         client.inFlightBytes,
		HighWaterEnvelopes:    client.highWaterEnvelopes,
		HighWaterEvents:       client.highWaterEvents,
		HighWaterBytes:        client.highWaterBytes,
		ReceivedEnvelopes:     client.receivedEnvelopes,
		ReceivedEvents:        client.receivedEvents,
		ReceivedBytes:         client.receivedBytes,
		DeliveredMessages:     client.deliveredMessages,
		DeliveredEnvelopes:    client.deliveredEnvelopes,
		DeliveredEvents:       client.deliveredEvents,
		DeliveredBytes:        client.deliveredBytes,
		CoalescedEnvelopes:    client.coalescedEnvelopes,
		DiscardedEnvelopes:    client.discardedEnvelopes,
		DiscardedEvents:       client.discardedEvents,
		DiscardedBytes:        client.discardedBytes,
		WriteCount:            client.writeCount,
		WriteErrors:           client.writeErrors,
		LastWriteBytes:        client.lastWriteBytes,
		LastWriteMilliseconds: client.lastWriteDuration.Milliseconds(),
		MaxWriteMilliseconds:  client.maxWriteDuration.Milliseconds(),
	}
}

func (h *Hub) Diagnostics() EventStreamDiagnostics {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	result := EventStreamDiagnostics{
		Limits:               eventStreamLimits(),
		ActiveSubscribers:    len(h.clients),
		Created:              h.stream.created,
		Removed:              h.stream.removed,
		HardLimitEvictions:   h.stream.hardLimitEvictions,
		CoalescedEnvelopes:   h.stream.coalescedEnvelopes,
		EncodingFailures:     h.stream.encodingFailures,
		Writes:               h.stream.writes,
		WriteErrors:          h.stream.writeErrors,
		MaxWriteMilliseconds: h.stream.maxWriteDuration.Milliseconds(),
		Subscribers:          make([]EventSubscriberDiagnostics, 0, len(h.clients)),
	}
	for _, client := range h.clients {
		result.Subscribers = append(result.Subscribers, subscriberDiagnostics(client, now))
	}
	sort.Slice(result.Subscribers, func(i, j int) bool {
		return result.Subscribers[i].ID < result.Subscribers[j].ID
	})
	return result
}

func encodedEnvelopeSize(envelope Envelope) (int, bool) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return subscriberBacklogMaxBytes + 1, false
	}
	return len(encoded), true
}

func envelopeEventCount(envelope Envelope) int {
	if envelope.Type != "events" {
		return 0
	}
	return len(envelope.Events)
}

func canCoalesceEventEnvelope(previous queuedEnvelope, next Envelope, nextBytes, nextEvents int) bool {
	if previous.envelope.Type != "events" || next.Type != "events" {
		return false
	}
	if previous.envelope.ToSequence == 0 || next.FromSequence == 0 ||
		previous.envelope.ToSequence+1 != next.FromSequence {
		return false
	}
	return previous.eventCount+nextEvents <= subscriberCoalescedEnvelopeMaxEvents &&
		previous.estimatedBytes+nextBytes <= subscriberCoalescedEnvelopeMaxBytes
}

func signalSubscriberReady(client *eventSubscriber) {
	select {
	case client.ready <- struct{}{}:
	default:
	}
}

func (h *Hub) enqueueLocked(client *eventSubscriber, envelope Envelope, estimatedBytes int) bool {
	eventCount := envelopeEventCount(envelope)
	client.receivedEnvelopes++
	client.receivedEvents += uint64(eventCount)
	client.receivedBytes += uint64(estimatedBytes)
	if client.queuedEvents+client.inFlightEvents+eventCount > subscriberBacklogMaxEvents ||
		client.queuedBytes+client.inFlightBytes+estimatedBytes > subscriberBacklogMaxBytes {
		var queuedSourceEnvelopes uint64
		for _, queued := range client.backlog {
			queuedSourceEnvelopes += queued.sourceEnvelopes
		}
		client.discardedEnvelopes += queuedSourceEnvelopes + 1
		client.discardedEvents += uint64(client.queuedEvents + eventCount)
		client.discardedBytes += uint64(client.queuedBytes + estimatedBytes)
		h.removeSubscriberLocked(client, eventStreamCloseSubscriberHardLimit)
		return false
	}

	if count := len(client.backlog); client.sequenceRanges && count > 0 &&
		canCoalesceEventEnvelope(client.backlog[count-1], envelope, estimatedBytes, eventCount) {
		last := &client.backlog[count-1]
		// The first queued event slice is subscriber-owned (see below), so
		// amortized append can grow it without mutating another browser's range.
		last.envelope.Events = append(last.envelope.Events, envelope.Events...)
		last.envelope.Sequence = envelope.ToSequence
		last.envelope.ToSequence = envelope.ToSequence
		last.estimatedBytes += estimatedBytes
		last.eventCount += eventCount
		last.sourceEnvelopes++
		client.coalescedEnvelopes++
		h.stream.coalescedEnvelopes++
	} else {
		queuedCopy := envelope
		if envelope.Type == "events" {
			queuedCopy.Events = append([]appgui.WireEvent(nil), envelope.Events...)
		}
		client.backlog = append(client.backlog, queuedEnvelope{
			envelope:        queuedCopy,
			estimatedBytes:  estimatedBytes,
			eventCount:      eventCount,
			sourceEnvelopes: 1,
		})
	}
	client.queuedEvents += eventCount
	client.queuedBytes += estimatedBytes
	if len(client.backlog) > client.highWaterEnvelopes {
		client.highWaterEnvelopes = len(client.backlog)
	}
	retainedEvents := client.queuedEvents + client.inFlightEvents
	retainedBytes := client.queuedBytes + client.inFlightBytes
	if retainedEvents > client.highWaterEvents {
		client.highWaterEvents = retainedEvents
	}
	if retainedBytes > client.highWaterBytes {
		client.highWaterBytes = retainedBytes
	}
	signalSubscriberReady(client)
	return true
}

func (h *Hub) nextDelivery(client *eventSubscriber) (subscriptionDelivery, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if client.removed || client.inFlight || len(client.backlog) == 0 {
		return subscriptionDelivery{}, false
	}
	queued := client.backlog[0]
	client.backlog[0] = queuedEnvelope{}
	client.backlog = client.backlog[1:]
	if len(client.backlog) == 0 {
		client.backlog = nil
	}
	client.queuedEvents -= queued.eventCount
	client.queuedBytes -= queued.estimatedBytes
	client.inFlight = true
	client.inFlightBytes = queued.estimatedBytes
	client.inFlightEvents = queued.eventCount
	client.inFlightEnvelopes = queued.sourceEnvelopes
	return subscriptionDelivery{
		Envelope:             queued.envelope,
		EstimatedBytes:       queued.estimatedBytes,
		EventCount:           queued.eventCount,
		SourceEnvelopes:      queued.sourceEnvelopes,
		QueuedEnvelopesAfter: len(client.backlog),
		QueuedEventsAfter:    client.queuedEvents,
		QueuedBytesAfter:     client.queuedBytes,
	}, true
}

func (h *Hub) removeSubscriberLocked(client *eventSubscriber, reason string) {
	if client == nil || client.removed {
		return
	}
	if current, ok := h.clients[client.id]; ok && current == client {
		delete(h.clients, client.id)
	}
	client.removed = true
	client.closeReason = reason
	if reason != eventStreamCloseSubscriberHardLimit {
		for _, queued := range client.backlog {
			client.discardedEnvelopes += queued.sourceEnvelopes
			client.discardedEvents += uint64(queued.eventCount)
			client.discardedBytes += uint64(queued.estimatedBytes)
		}
	}
	for index := range client.backlog {
		client.backlog[index] = queuedEnvelope{}
	}
	client.backlog = nil
	client.queuedEvents = 0
	client.queuedBytes = 0
	h.stream.removed++
	if reason == eventStreamCloseSubscriberHardLimit {
		h.stream.hardLimitEvictions++
	}
	close(client.closed)
}
