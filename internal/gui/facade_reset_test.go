package gui

import (
	"testing"
	"time"

	"github.com/cyber-godzilla/praetor/internal/client"
	"github.com/cyber-godzilla/praetor/internal/config"
	"github.com/cyber-godzilla/praetor/internal/types"
)

func emittedKinds(em *captureEmitter) []string {
	var kinds []string
	for _, e := range em.snapshot() {
		if batch, ok := e.data.([]WireEvent); ok {
			for _, w := range batch {
				kinds = append(kinds, w.Kind)
			}
		}
	}
	return kinds
}

func hasKind(kinds []string, k string) bool {
	for _, got := range kinds {
		if got == k {
			return true
		}
	}
	return false
}

func skootRooms() types.SKOOTUpdateEvent {
	return types.SKOOTUpdateEvent{
		Rooms: []types.MinimapRoom{{X: 0, Y: 0, Size: 10, Color: "#ffffff", Brightness: 20}},
	}
}

func TestFacade_DisconnectSkipsTrailingSKOOTInBatch(t *testing.T) {
	em := &captureEmitter{}
	a := NewGuiApp(&Deps{Config: config.Defaults()}, em)

	// Disconnect then a trailing SKOOT in the same batch: the reset must stick,
	// so no minimap is re-emitted.
	a.processBatch([]types.Event{
		types.DisconnectedEvent{Reason: "connection closed"},
		skootRooms(),
	})

	if hasKind(emittedKinds(em), KindMinimap) {
		t.Error("trailing SKOOT repopulated the minimap after a disconnect")
	}
}

func TestFacade_SKOOTBeforeDisconnectStillApplies(t *testing.T) {
	em := &captureEmitter{}
	a := NewGuiApp(&Deps{Config: config.Defaults()}, em)

	// SKOOT before the disconnect must apply normally (minimap emitted), then reset.
	a.processBatch([]types.Event{
		skootRooms(),
		types.DisconnectedEvent{Reason: "connection closed"},
	})

	if !hasKind(emittedKinds(em), KindMinimap) {
		t.Error("SKOOT before a disconnect was dropped; it should apply then reset")
	}
}

func TestFacade_DeduplicatesSemanticStatusDuringTextBurst(t *testing.T) {
	em := &captureEmitter{}
	cfg := config.Defaults()
	notifier := client.NewDesktopNotifier(cfg.Notifications.Desktop)
	notifier.SetSink(nil)
	a := NewGuiApp(&Deps{Config: cfg, DesktopNotify: notifier}, em)
	started := time.Now().Add(-time.Minute)
	status := func(value int) types.StatusUpdateEvent {
		return types.StatusUpdateEvent{
			Mode: "practice",
			MetricsCurrent: &types.MetricSnapshot{
				Mode:  "practice",
				Start: started,
				Entries: []types.MetricSnapshotEntry{{
					Label: "actions", Value: value,
				}},
			},
			MetricsHistory: []types.MetricSnapshot{{
				Mode: "prior", Start: started.Add(-time.Hour),
				End: started.Add(-59 * time.Minute),
			}},
		}
	}

	burst := make([]types.Event, 0, 401)
	burst = append(burst, status(1))
	for index := 0; index < 200; index++ {
		burst = append(burst,
			types.GameTextEvent{Text: "inventory line"},
			status(1),
		)
	}
	a.processBatch(burst)

	statusCount, textCount := 0, 0
	for _, emitted := range em.snapshot() {
		for _, event := range emitted.data.([]WireEvent) {
			switch event.Kind {
			case KindStatus:
				statusCount++
			case KindText:
				textCount++
			}
		}
	}
	if statusCount != 1 || textCount != 200 {
		t.Fatalf("emitted status=%d text=%d, want 1/200", statusCount, textCount)
	}

	// A real metric mutation must still propagate.
	a.processBatch([]types.Event{status(2)})
	if kinds := emittedKinds(em); len(kinds) == 0 || kinds[len(kinds)-1] != KindStatus {
		t.Fatalf("metric mutation did not emit status: %v", kinds)
	}

	// Disconnect resets the cache so a new game session can establish its
	// status even if it begins with structurally identical metrics.
	a.processBatch([]types.Event{types.DisconnectedEvent{Reason: "test"}})
	a.processBatch([]types.Event{status(2)})
	statusCount = 0
	for _, kind := range emittedKinds(em) {
		if kind == KindStatus {
			statusCount++
		}
	}
	if statusCount != 3 {
		t.Fatalf("status count after mutation and reconnect = %d, want 3", statusCount)
	}
}
