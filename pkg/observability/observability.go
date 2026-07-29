package observability

import (
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/applog"
)

// Event is one observability record.
type Event struct {
	Op       string
	Phase    string
	Duration time.Duration
	Err      error
	Attrs    map[string]any
}

// Sink receives events.
type Sink func(Event)

// DefaultSink logs through applog.
func DefaultSink(ev Event) {
	if ev.Err != nil {
		applog.Printf("obs op=%s phase=%s dur=%s err=%v attrs=%v", ev.Op, ev.Phase, ev.Duration, ev.Err, ev.Attrs)
		return
	}
	applog.Printf("obs op=%s phase=%s dur=%s attrs=%v", ev.Op, ev.Phase, ev.Duration, ev.Attrs)
}

// Wrap times fn and reports to sink.
func Wrap(sink Sink, op string, fn func() error) error {
	if sink == nil {
		sink = DefaultSink
	}
	start := time.Now()
	sink(Event{Op: op, Phase: "start", Attrs: nil})
	err := fn()
	sink(Event{Op: op, Phase: "end", Duration: time.Since(start), Err: err})
	return err
}
