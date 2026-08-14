//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"core/shared/config"
	"golang.org/x/sys/unix"
)

type serviceChildContainment struct {
	lock      *os.File
	lease     *os.File
	leaseLost chan struct{}
	closeOnce sync.Once
}

func (c *serviceChildContainment) startLeaseMonitor() {
	if c == nil || c.lease == nil || c.leaseLost == nil {
		return
	}
	go func() {
		message, err := readDarwinServiceMessage(c.lease)
		if err != nil || message.Kind != "settling" {
			close(c.leaseLost)
		}
	}()
}

func prepareServiceChildInvocation(persistenceRoot string) (serviceChildContainment, error) {
	marker := strings.TrimSpace(os.Getenv(darwinServiceChildMarkerEnv))
	lockRaw := strings.TrimSpace(os.Getenv(darwinServiceLockFDEnv))
	leaseRaw := strings.TrimSpace(os.Getenv(darwinServiceLeaseFDEnv))
	gateRaw := strings.TrimSpace(os.Getenv(darwinServiceGateFDEnv))
	for _, name := range []string{darwinServiceChildMarkerEnv, darwinServiceLockFDEnv, darwinServiceLeaseFDEnv, darwinServiceGateFDEnv} {
		_ = os.Unsetenv(name)
	}
	if marker == "" && lockRaw == "" && leaseRaw == "" && gateRaw == "" {
		return serviceChildContainment{}, nil
	}
	if marker != "1" || lockRaw == "" || leaseRaw == "" || gateRaw == "" {
		return serviceChildContainment{}, errors.New("invalid private Darwin service-child invocation")
	}
	lockFD, err := strconv.Atoi(lockRaw)
	if err != nil || lockFD < darwinInheritedFDBase {
		return serviceChildContainment{}, errors.New("invalid private Darwin service lock descriptor")
	}
	leaseFD, err := strconv.Atoi(leaseRaw)
	if err != nil || leaseFD < darwinInheritedFDBase || leaseFD == lockFD {
		return serviceChildContainment{}, errors.New("invalid private Darwin watchdog lease descriptor")
	}
	gateFD, err := strconv.Atoi(gateRaw)
	if err != nil || gateFD < darwinInheritedFDBase || gateFD == lockFD || gateFD == leaseFD {
		return serviceChildContainment{}, errors.New("invalid private Darwin start-gate descriptor")
	}
	for _, fd := range []int{lockFD, leaseFD, gateFD} {
		unix.CloseOnExec(fd)
	}
	lock := os.NewFile(uintptr(lockFD), "Darwin service activation lock")
	lease := os.NewFile(uintptr(leaseFD), "Darwin service watchdog lease")
	gate := os.NewFile(uintptr(gateFD), "Darwin service start gate")
	if lock == nil || lease == nil || gate == nil {
		closeDarwinFiles(lock, lease, gate)
		return serviceChildContainment{}, errors.New("adopt private Darwin service descriptors")
	}
	root, err := effectiveDarwinServiceRoot(persistenceRoot)
	if err != nil {
		closeDarwinFiles(lock, lease, gate)
		return serviceChildContainment{}, err
	}
	if err := validateDarwinLockedFile(lock, darwinServiceLockPath(root)); err != nil {
		closeDarwinFiles(lock, lease, gate)
		return serviceChildContainment{}, err
	}
	watchdogAck, err := readDarwinServiceMessage(lease)
	if err != nil {
		closeDarwinFiles(lock, lease, gate)
		return serviceChildContainment{}, fmt.Errorf("wait for Darwin watchdog acknowledgement: %w", err)
	}
	gateAck, err := readDarwinServiceMessage(gate)
	_ = gate.Close()
	if err != nil {
		closeDarwinFiles(lock, lease)
		return serviceChildContainment{}, fmt.Errorf("wait for Darwin host start gate: %w", err)
	}
	if err := validateDarwinArmedAcknowledgement(watchdogAck, os.Getpid()); err != nil {
		closeDarwinFiles(lock, lease)
		return serviceChildContainment{}, err
	}
	if gateAck.Kind != watchdogAck.Kind || gateAck.HostPID != watchdogAck.HostPID || gateAck.ChildPID != watchdogAck.ChildPID {
		closeDarwinFiles(lock, lease)
		return serviceChildContainment{}, errors.New("Darwin host and watchdog acknowledgements disagree")
	}
	containment := serviceChildContainment{lock: lock, lease: lease, leaseLost: make(chan struct{})}
	containment.startLeaseMonitor()
	return containment, nil
}

func validateDarwinArmedAcknowledgement(message darwinServiceMessage, childPID int) error {
	if message.Kind != "armed" || message.HostPID <= 0 || message.ChildPID != childPID {
		return errors.New("invalid Darwin watchdog acknowledgement")
	}
	return nil
}

func effectiveDarwinServiceRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(strings.TrimSpace(explicit))
	}
	return config.ResolvePersistenceRoot("")
}

func darwinServiceRuntimeDir(persistenceRoot string) string {
	return filepath.Join(persistenceRoot, "service")
}

func darwinServiceLockPath(persistenceRoot string) string {
	return filepath.Join(darwinServiceRuntimeDir(persistenceRoot), "darwin-activation.lock")
}

func (c *serviceChildContainment) Context(parent context.Context) context.Context {
	if c == nil || c.leaseLost == nil {
		return parent
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-c.leaseLost:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

func (c *serviceChildContainment) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		closeDarwinFiles(c.lease, c.lock)
	})
}
