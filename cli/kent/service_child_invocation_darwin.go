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

func prepareServiceChildInvocation(ctx context.Context, persistenceRoot string) (serviceChildContainment, error) {
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
	armingCtx, cancel := context.WithTimeout(ctx, darwinServiceHandshakeTimeout)
	defer cancel()
	watchdogAck, gateAck, err := waitForDarwinChildArming(armingCtx, lease, gate)
	_ = gate.Close()
	if err != nil {
		closeDarwinFiles(lock, lease)
		return serviceChildContainment{}, err
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

func waitForDarwinChildArming(ctx context.Context, lease, gate *os.File) (darwinServiceMessage, darwinServiceMessage, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("create Darwin child arming queue: %w", err)
	}
	defer unix.Close(queue)
	wake, err := darwinSocketPair()
	if err != nil {
		return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("create Darwin child arming cancellation channel: %w", err)
	}
	defer func() {
		_ = unix.Close(wake[0])
		_ = unix.Close(wake[1])
	}()
	changes := []unix.Kevent_t{
		{Ident: uint64(lease.Fd()), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
		{Ident: uint64(gate.Fd()), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
		{Ident: uint64(wake[0]), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
	}
	if _, err := unix.Kevent(queue, changes, nil, nil); err != nil {
		return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("arm Darwin child startup channels: %w", err)
	}
	var watchdogAck darwinServiceMessage
	watchdogReady := false
	cancelWakeDone := make(chan struct{})
	defer close(cancelWakeDone)
	go func() {
		select {
		case <-ctx.Done():
			_, _ = unix.Write(wake[1], []byte{1})
		case <-cancelWakeDone:
		}
	}()
	events := make([]unix.Kevent_t, 3)
	for {
		count, err := unix.Kevent(queue, nil, events, nil)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("wait for Darwin child startup: %w", err)
		}
		for _, event := range events[:count] {
			switch int(event.Ident) {
			case wake[0]:
				return darwinServiceMessage{}, darwinServiceMessage{}, ctx.Err()
			case int(lease.Fd()):
				message, err := readDarwinServiceMessageContext(ctx, lease)
				if err != nil {
					if watchdogReady {
						return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("Darwin watchdog lease failed before host start gate: %w", err)
					}
					return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("wait for Darwin watchdog acknowledgement: %w", err)
				}
				if watchdogReady {
					return darwinServiceMessage{}, darwinServiceMessage{}, errors.New("unexpected Darwin watchdog message before host start gate")
				}
				watchdogAck = message
				watchdogReady = true
			case int(gate.Fd()):
				gateAck, err := readDarwinServiceMessageContext(ctx, gate)
				if err != nil {
					return darwinServiceMessage{}, darwinServiceMessage{}, fmt.Errorf("wait for Darwin host start gate: %w", err)
				}
				if !watchdogReady {
					return darwinServiceMessage{}, darwinServiceMessage{}, errors.New("Darwin host opened the start gate before watchdog acknowledgement")
				}
				return watchdogAck, gateAck, nil
			}
		}
	}
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
