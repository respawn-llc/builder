//go:build windows

package main

import (
	"context"
	brand "core/shared/config"
	"fmt"
	"io"

	"golang.org/x/sys/windows/svc"
)

// serviceHostRun is the SCM entry point. The service manager launches the
// registered command (`kent service run --persistence-root <root>`); svc.Run
// connects this process to the service control dispatcher and drives the
// supervisor until the SCM stops it. Invoked by hand it reports that it is not
// running under the SCM.
func serviceHostRun(spec serviceSpec, _ io.Writer, stderr io.Writer) int {
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(stderr, "determine service context: %v\n", err)
		return 1
	}
	if !isService {
		fmt.Fprintf(stderr, "%s service run is an internal entry invoked by the Windows service manager; use `%s service start` instead\n", brand.Command, brand.Command)
		return 2
	}
	if err := svc.Run(serviceWindowsServiceName, &windowsServiceHandler{spec: spec}); err != nil {
		fmt.Fprintf(stderr, "service run: %v\n", err)
		return 1
	}
	return 0
}

// serviceStopWaitHintMillis tells the SCM how long the service may take to stop
// so it waits for the graceful server shutdown (event signal + grace period +
// hard-terminate fallback) instead of killing the supervisor, which would orphan
// the server.
const serviceStopWaitHintMillis = 30000

type windowsServiceHandler struct {
	spec serviceSpec
}

func (h *windowsServiceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newServerSupervisor(h.spec)
	supervisor.setWanted(activeUserSession())

	done := make(chan struct{})
	go func() {
		supervisor.run(ctx)
		close(done)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case <-done:
			return false, 0
		case request := <-r:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending, WaitHint: serviceStopWaitHintMillis}
				cancel()
				<-done
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			case svc.SessionChange:
				// Any logon/logoff/lock change: recompute the target interactive
				// session (0 when no user is logged in) and let the supervisor
				// relaunch or stop the server accordingly.
				supervisor.setWanted(activeUserSession())
			}
		}
	}
}
