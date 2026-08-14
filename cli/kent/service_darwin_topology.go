//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"

	"core/shared/protocol"
	"golang.org/x/sys/unix"
)

type darwinLaunchdHost struct {
	Loaded  bool
	PID     int
	State   string
	Command []string
}

type darwinOwnedChild struct {
	PID     int
	Command []string
}

type darwinServiceTopology struct {
	Host  darwinLaunchdHost
	Child *darwinOwnedChild
}

func resolveDarwinLaunchdHost(ctx context.Context, spec serviceSpec) (darwinLaunchdHost, error) {
	inspection, err := inspectLaunchdService(ctx)
	if err != nil {
		return darwinLaunchdHost{}, err
	}
	if !inspection.Loaded {
		return darwinLaunchdHost{}, nil
	}
	output := inspection.Output
	command := parseLaunchdPrintProgramArguments(output)
	expected := darwinServiceHostCommand(spec)
	if !commandArgsEqual(command, expected) {
		return darwinLaunchdHost{}, fmt.Errorf("loaded launchd host command is %s, expected %s", commandString(command), commandString(expected))
	}
	pid := launchdPID(output)
	if pid > 0 {
		alive, err := darwinProcessAlive(pid)
		if err != nil {
			return darwinLaunchdHost{}, fmt.Errorf("inspect loaded launchd host %d: %w", pid, err)
		}
		if !alive {
			return darwinLaunchdHost{}, fmt.Errorf("launchd reports host process %d, but it is not alive", pid)
		}
		identity, err := inspectDarwinProcessIdentity(pid)
		if err != nil {
			if errors.Is(err, unix.ESRCH) {
				return darwinLaunchdHost{}, fmt.Errorf("launchd host process %d exited during inspection", pid)
			}
			return darwinLaunchdHost{}, err
		}
		if !commandArgsEqual(identity.Command, expected) {
			return darwinLaunchdHost{}, fmt.Errorf("process %d command is %s, expected %s", pid, commandString(identity.Command), commandString(expected))
		}
	}
	return darwinLaunchdHost{Loaded: true, PID: pid, State: launchdState(output), Command: command}, nil
}

func resolveDarwinHostChild(spec serviceSpec, host darwinLaunchdHost) (*darwinOwnedChild, error) {
	if !host.Loaded || host.PID == 0 {
		return nil, nil
	}
	children, err := listDarwinChildProcesses(host.PID)
	if err != nil {
		return nil, err
	}
	expected := serverChildCommand(spec)
	var owned *darwinOwnedChild
	for _, child := range children {
		if !commandArgsEqual(child.Command, expected) {
			continue
		}
		if owned != nil {
			return nil, fmt.Errorf("launchd host %d owns multiple matching server children (%d and %d)", host.PID, owned.PID, child.PID)
		}
		owned = &darwinOwnedChild{PID: child.PID, Command: append([]string(nil), child.Command...)}
	}
	return owned, nil
}

func resolveDarwinServiceTopology(ctx context.Context, spec serviceSpec) (darwinServiceTopology, error) {
	host, err := resolveDarwinLaunchdHost(ctx, spec)
	if err != nil {
		return darwinServiceTopology{}, err
	}
	child, err := resolveDarwinHostChild(spec, host)
	if err != nil {
		return darwinServiceTopology{}, err
	}
	return darwinServiceTopology{Host: host, Child: child}, nil
}

func resolveDarwinServiceReadiness(ctx context.Context, spec serviceSpec) error {
	topology, err := resolveDarwinServiceTopology(ctx, spec)
	if err != nil {
		return err
	}
	healthStatus, healthPID := probeServiceHealth(ctx, spec)
	return validateDarwinServiceReadiness(topology, healthStatus, healthPID)
}

func validateDarwinServiceReadiness(topology darwinServiceTopology, healthStatus string, healthPID int) error {
	if topology.Child == nil {
		return errors.New("launchd host has not started its server child")
	}
	if healthStatus != protocol.HealthStatusOK || healthPID != topology.Child.PID {
		return fmt.Errorf("owned server child %d is not ready (health=%s health_pid=%d)", topology.Child.PID, healthStatus, healthPID)
	}
	return nil
}
