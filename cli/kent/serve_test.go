package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"core/server/metadata"
	serverstartup "core/server/startup"
)

type serveCommandServerStub struct {
	err error
}

func (s serveCommandServerStub) Close() error {
	return nil
}

func (s serveCommandServerStub) Serve(context.Context) error {
	return s.err
}

func TestServeSubcommandMapsOnlyCriticalInfrastructureTerminationToStatusTwo(t *testing.T) {
	originalStart := startServeServer
	originalHandlers := newServeStartupHandlers
	t.Cleanup(func() {
		startServeServer = originalStart
		newServeStartupHandlers = originalHandlers
	})
	newServeStartupHandlers = func() (serverstartup.AuthHandler, serverstartup.OnboardingHandler) {
		return nil, nil
	}

	tests := []struct {
		name       string
		serveErr   error
		wantStatus int
	}{
		{
			name: "critical metadata termination",
			serveErr: &serverstartup.CriticalInfrastructureTermination{
				Cause: &metadata.ClassifiedFailure{
					Class:     metadata.FailureCritical,
					Operation: "Session append projection",
					Cause:     errors.New("database full"),
				},
			},
			wantStatus: 2,
		},
		{
			name: "critical metadata termination wrapping cancellation",
			serveErr: &serverstartup.CriticalInfrastructureTermination{
				Cause: &metadata.ClassifiedFailure{
					Class:     metadata.FailureCritical,
					Operation: "metadata read",
					Cause:     context.Canceled,
				},
			},
			wantStatus: 2,
		},
		{
			name:       "ordinary serving failure",
			serveErr:   errors.New("listener failed"),
			wantStatus: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startServeServer = func(
				context.Context,
				serverstartup.Request,
				serverstartup.AuthHandler,
				serverstartup.OnboardingHandler,
			) (serveCommandServer, error) {
				return serveCommandServerStub{err: tt.serveErr}, nil
			}
			var stderr strings.Builder
			if got := serveSubcommand(nil, io.Discard, &stderr); got != tt.wantStatus {
				t.Fatalf("serve status = %d, want %d; stderr=%q", got, tt.wantStatus, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("serve did not emit a diagnostic")
			}
		})
	}
}
