package harness

import (
	"sync"
	"time"
)

type HarnessEventType string

const (
	HarnessRunStart HarnessEventType = "run_start"
	HarnessRunEnd   HarnessEventType = "run_end"
)

type HarnessEvent struct {
	Type    HarnessEventType
	RunID   string
	Outcome string
	Time    time.Time
}

type HarnessEventBus struct {
	mu       sync.Mutex
	history  []HarnessEvent
	nextID   int
	watchers map[int]chan HarnessEvent
}

func NewHarnessEventBus() *HarnessEventBus {
	return &HarnessEventBus{watchers: map[int]chan HarnessEvent{}}
}

func (b *HarnessEventBus) Publish(event HarnessEvent) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	b.mu.Lock()
	b.history = append(b.history, event)
	for _, ch := range b.watchers {
		select {
		case ch <- event:
		default:
		}
	}
	b.mu.Unlock()
}

// Watch atomically returns the current snapshot and subscribes to later events.
func (b *HarnessEventBus) Watch() (snapshot []HarnessEvent, events <-chan HarnessEvent, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot = append([]HarnessEvent(nil), b.history...)
	id := b.nextID
	b.nextID++
	ch := make(chan HarnessEvent, 64)
	b.watchers[id] = ch
	cancel = func() {
		b.mu.Lock()
		if current, ok := b.watchers[id]; ok {
			delete(b.watchers, id)
			close(current)
		}
		b.mu.Unlock()
	}
	return snapshot, ch, cancel
}
