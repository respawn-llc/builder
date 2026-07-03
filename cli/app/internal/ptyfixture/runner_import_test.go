package ptyfixture

import (
	"context"
	"testing"

	"core/cli/app/internal/runner"
)

type importProofServer struct{}

func (s *importProofServer) Close() error { return nil }

func TestPtyFixtureCanUseInternalRunner(t *testing.T) {
	deps := runner.Dependencies[*importProofServer, struct{}, runner.NoStartupOptions]{
		NewAuthInteractor: func() struct{} { return struct{}{} },
		StartSessionServer: func(context.Context, runner.Request[runner.NoStartupOptions], struct{}, bool) (*importProofServer, error) {
			return &importProofServer{}, nil
		},
		RunSessionLifecycle: func(context.Context, *importProofServer, struct{}, string, runner.SessionLifecycleOptions) error {
			return nil
		},
	}
	if err := runner.RunInteractive(context.Background(), runner.Request[runner.NoStartupOptions]{}, deps); err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
}
