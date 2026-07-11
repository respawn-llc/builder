//go:build windows

package main

import (
	"context"
	brand "core/shared/config"
	"fmt"
	"io"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

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

const (
	serviceStopWindow         = 30 * time.Second
	serviceStopWaitHintMillis = uint32(serviceStopWindow / time.Millisecond)
)

type windowsServiceHandler struct {
	spec serviceSpec
}

func (h *windowsServiceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newServerSupervisor(h.spec)
	supervisor.setWanted(supervisor.targetSession())

	done := make(chan struct{})
	go func() {
		supervisor.run(ctx)
		close(done)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case <-done:
			changes <- svc.Status{State: svc.Stopped}
			return false, uint32(windows.ERROR_PROCESS_ABORTED)
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

				supervisor.setWanted(supervisor.targetSession())
			}
		}
	}
}
