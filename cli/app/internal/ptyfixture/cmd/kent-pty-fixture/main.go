package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"core/cli/app"
	"core/cli/app/internal/apphooks"
	"core/cli/app/internal/ptyfixture"
	"core/cli/app/internal/runner"
	"core/internal/testharness/pty/appfixture"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags, err := parseFixtureFlags(args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(flags.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	if err := appfixture.PrepareConfigAndBinding(ctx, flags.PersistenceRoot, flags.WorkspaceRoot); err != nil {
		return err
	}
	directMatrix, err := scriptRequestsDirectMatrix(flags.ScriptPath)
	if err != nil {
		return err
	}
	if directMatrix {
		return runDirectMatrixFixture(flags.ObservationPath)
	}
	observer := ptyfixture.NewTerminalPhaseMarkerObserver()
	runtime, err := appfixture.NewRuntime(flags.ScriptPath, func(ctx context.Context) error {
		sink, err := observer.Wait(ctx)
		if err != nil {
			return err
		}
		return sink.RequestTerminalPhaseMarker(runner.TerminalPhaseMarker{Phase: runner.TerminalPhaseScenarioComplete})
	})
	if err != nil {
		return err
	}
	ctx = apphooks.WithOptions(ctx, apphooks.Options{
		StartupOptions:                  runtime.StartupOptions(),
		TerminalPhaseMarkerEncoder:      &ptyfixture.PhaseMarkerEncoder{Raw: appfixture.RawPhaseMarkerEncoder{}},
		TerminalPhaseMarkerSinkObserver: observer,
	})
	runErr := app.Run(ctx, app.Options{
		WorkspaceRoot:         flags.WorkspaceRoot,
		ConfigRoot:            flags.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
	})
	if err := appfixture.WriteObservation(flags.ObservationPath, runtime.Observation(runErr)); err != nil {
		return errors.Join(runErr, fmt.Errorf("write observation: %w", err))
	}
	return runErr
}
