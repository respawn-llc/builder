package ptyfixture

import (
	"context"
	"fmt"
	"sync"

	"core/cli/app/internal/runner"

	"github.com/google/uuid"
)

type RawPhaseMarkerEncoder interface {
	EncodeRawPhaseMarker(sequence int, phase string, windowID *uuid.UUID) ([]byte, error)
}

type PhaseMarkerEncoder struct {
	mu       sync.Mutex
	sequence int
	Raw      RawPhaseMarkerEncoder
}

func (e *PhaseMarkerEncoder) EncodeTerminalPhaseMarker(marker runner.TerminalPhaseMarker) ([]byte, error) {
	if e.Raw == nil {
		return nil, fmt.Errorf("raw phase marker encoder is required")
	}
	if err := marker.Validate(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sequence++
	return e.Raw.EncodeRawPhaseMarker(e.sequence, string(marker.Phase), marker.WindowID)
}

type TerminalPhaseMarkerObserver struct {
	once sync.Once
	ch   chan runner.TerminalPhaseMarkerSink
}

func NewTerminalPhaseMarkerObserver() *TerminalPhaseMarkerObserver {
	return &TerminalPhaseMarkerObserver{ch: make(chan runner.TerminalPhaseMarkerSink, 1)}
}

func (o *TerminalPhaseMarkerObserver) TerminalPhaseMarkerSinkReady(ctx context.Context, sink runner.TerminalPhaseMarkerSink) error {
	if o == nil || sink == nil {
		return nil
	}
	o.once.Do(func() {
		o.ch <- sink
	})
	return sink.RequestTerminalPhaseMarker(runner.TerminalPhaseMarker{Phase: runner.TerminalPhaseScenarioStart})
}

func (o *TerminalPhaseMarkerObserver) Wait(ctx context.Context) (runner.TerminalPhaseMarkerSink, error) {
	if o == nil {
		return nil, fmt.Errorf("terminal phase marker observer is required")
	}
	select {
	case sink := <-o.ch:
		o.ch <- sink
		return sink, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
