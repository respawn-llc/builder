//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func runDarwinWatchdog(args []string) error {
	if len(args) != 2 {
		return errors.New("invalid Darwin watchdog invocation")
	}
	hostFD, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || hostFD < darwinInheritedFDBase {
		return errors.New("invalid Darwin watchdog host lease descriptor")
	}
	childFD, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || childFD < darwinInheritedFDBase || childFD == hostFD {
		return errors.New("invalid Darwin watchdog child lease descriptor")
	}
	hostLease := os.NewFile(uintptr(hostFD), "Darwin watchdog host lease")
	childLease := os.NewFile(uintptr(childFD), "Darwin watchdog child lease")
	if hostLease == nil || childLease == nil {
		closeDarwinFiles(hostLease, childLease)
		return errors.New("adopt Darwin watchdog lease descriptors")
	}
	defer closeDarwinFiles(hostLease, childLease)
	unix.CloseOnExec(hostFD)
	unix.CloseOnExec(childFD)

	setup, err := readDarwinServiceMessage(hostLease)
	if err != nil {
		return fmt.Errorf("read Darwin watchdog setup: %w", err)
	}
	if setup.Kind != "setup" || setup.HostPID <= 0 || setup.ChildPID <= 0 {
		return errors.New("invalid Darwin watchdog setup")
	}
	if err := validateDarwinProcessIdentity(setup.HostPID, setup.HostCommand); err != nil {
		return fmt.Errorf("validate Darwin service host: %w", err)
	}
	child, err := inspectDarwinProcess(setup.ChildPID)
	if err != nil {
		return fmt.Errorf("validate Darwin server child: %w", err)
	}
	if child.Parent != setup.HostPID || !commandArgsEqual(child.Command, setup.ChildCommand) {
		return errors.New("Darwin watchdog server child identity does not match its host")
	}
	kqueue, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("create Darwin watchdog kqueue: %w", err)
	}
	defer unix.Close(kqueue)
	changes := []unix.Kevent_t{
		{Ident: uint64(setup.HostPID), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE, Fflags: unix.NOTE_EXIT},
		{Ident: uint64(setup.ChildPID), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE, Fflags: unix.NOTE_EXIT},
		{Ident: uint64(hostFD), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
		{Ident: uint64(childFD), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
	}
	if _, err := unix.Kevent(kqueue, changes, nil, nil); err != nil {
		return fmt.Errorf("arm Darwin watchdog: %w", err)
	}
	ack := darwinServiceMessage{Kind: "armed", HostPID: setup.HostPID, ChildPID: setup.ChildPID}
	if err := writeDarwinServiceMessage(hostLease, ack); err != nil {
		return fmt.Errorf("acknowledge Darwin service host: %w", err)
	}
	if err := writeDarwinServiceMessage(childLease, ack); err != nil {
		return fmt.Errorf("acknowledge Darwin server child: %w", err)
	}

	events := make([]unix.Kevent_t, 4)
	for {
		count, err := unix.Kevent(kqueue, nil, events, nil)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("observe Darwin service activation: %w", err)
		}
		hostExited := false
		childExited := false
		for _, event := range events[:count] {
			hostExited = hostExited || event.Filter == unix.EVFILT_PROC && int(event.Ident) == setup.HostPID
			childExited = childExited || event.Filter == unix.EVFILT_PROC && int(event.Ident) == setup.ChildPID
		}
		if childExited {
			if err := writeDarwinServiceMessage(hostLease, darwinServiceMessage{Kind: "child-exited"}); err != nil {
				return err
			}
			message, err := readDarwinServiceMessage(hostLease)
			if err != nil {
				return err
			}
			if message.Kind != "settling" {
				return errors.New("Darwin watchdog received an invalid child-settlement acknowledgement")
			}
			return nil
		}
		if hostExited {
			return watchdogSettleOrphan(setup.ChildPID, childLease)
		}
		for _, event := range events[:count] {
			if event.Filter != unix.EVFILT_READ {
				continue
			}
			switch int(event.Ident) {
			case hostFD:
				message, readErr := readDarwinServiceMessage(hostLease)
				if readErr == nil && message.Kind == "settling" {
					_ = writeDarwinServiceMessage(childLease, darwinServiceMessage{Kind: "settling"})
					return nil
				}
				return errors.New("Darwin watchdog lost its host lease")
			case childFD:
				message, readErr := readDarwinServiceMessage(childLease)
				if readErr == nil && message.Kind == "settling" {
					return nil
				}
				return errors.New("Darwin watchdog lost its child lease")
			}
		}
	}
}

func watchdogSettleOrphan(childPID int, childLease *os.File) error {
	_ = writeDarwinServiceMessage(childLease, darwinServiceMessage{Kind: "settling"})
	if err := syscall.Kill(childPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop orphaned Darwin server child %d: %w", childPID, err)
	}
	timeout := launchdServiceShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	exited, err := waitForDarwinProcessExitEvent(childPID, timeout)
	if err != nil {
		return err
	}
	if exited {
		return nil
	}
	if err := syscall.Kill(childPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force stop orphaned Darwin server child %d: %w", childPID, err)
	}
	exited, err = waitForDarwinProcessExitEvent(childPID, timeout)
	if err != nil {
		return err
	}
	if !exited {
		return fmt.Errorf("Darwin server child %d did not exit after forced termination", childPID)
	}
	return nil
}

func waitForDarwinProcessExitEvent(pid int, timeout time.Duration) (bool, error) {
	alive, err := darwinProcessAlive(pid)
	if err != nil || !alive {
		return !alive, err
	}
	queue, err := unix.Kqueue()
	if err != nil {
		return false, err
	}
	defer unix.Close(queue)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE, Fflags: unix.NOTE_EXIT}
	event := make([]unix.Kevent_t, 1)
	seconds := timeout / time.Second
	nanoseconds := timeout % time.Second
	wait := unix.Timespec{Sec: int64(seconds), Nsec: int64(nanoseconds)}
	count, err := unix.Kevent(queue, []unix.Kevent_t{change}, event, &wait)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return true, nil
		}
		return false, err
	}
	return count == 1, nil
}
