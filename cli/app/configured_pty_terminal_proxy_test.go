package app

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	checkpoint "core/internal/testharness/pty/analyzer"
)

func TestConfiguredPTYTerminalProxyCancellationReleasesFrameForwarder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan configuredPTYNativeFrameCheckpointResult, 1)
	proxy := &configuredPTYTerminalProxy{
		writer: checkpoint.NewWriter(&bytes.Buffer{}),
		pending: []configuredPTYAfterFrameCheckpoint{{
			kind:      checkpoint.KindMainUIReady,
			ctx:       ctx,
			predicate: func([]checkpoint.Cell) bool { return true },
			release:   make(chan struct{}),
			done:      done,
		}},
	}
	returned := make(chan struct{})
	go func() {
		proxy.afterNativeFrame(nil)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("canceled native-frame checkpoint blocked the terminal forwarder")
	}
	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled native-frame checkpoint error = %v, want context canceled", result.err)
	}
}
