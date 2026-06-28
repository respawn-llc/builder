package main

import (
	"context"
	serverstartup "core/server/startup"
	brand "core/shared/config"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

type serveCommandServer = ServeServer

var startServeServer = func(ctx context.Context, req serverstartup.Request, authHandler serverstartup.AuthHandler, onboardingHandler serverstartup.OnboardingHandler) (serveCommandServer, error) {
	return serverstartup.StartServeServer(ctx, req, authHandler, onboardingHandler)
}
var newServeStartupHandlers = func() (serverstartup.AuthHandler, serverstartup.OnboardingHandler) {
	return serverstartup.NewHeadlessHandlers(nil)
}

func serveSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	serveFS := newCommandFlagSet(brand.Command+" serve", stderr, serveUsage)
	persistenceRoot := serveFS.String("persistence-root", "", "config and data root directory (overrides KENT_PERSISTENCE_ROOT and the default ~/.kent)")
	if ok, exitCode := parseCommandFlags(serveFS, args); !ok {
		return exitCode
	}
	if remaining := serveFS.Args(); len(remaining) > 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(remaining, " "))
		serveFS.Usage()
		return 2
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = installServiceShutdownTrigger(ctx)
	authHandler, onboardingHandler := newServeStartupHandlers()
	server, err := startServeServer(ctx, serverstartup.Request{
		AllowUnauthenticated: true,
		LoadOptions:          brand.LoadOptions{ConfigRoot: strings.TrimSpace(*persistenceRoot)},
	}, authHandler, onboardingHandler)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	defer func() { _ = server.Close() }()
	_, _ = fmt.Fprintln(stderr, "Server started, Ctrl+C to stop")
	if err := server.Serve(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
