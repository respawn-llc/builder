// Package ownedprocess owns a noninteractive child process and its descendants.
package ownedprocess

import (
	"errors"
	"io"
	"os/exec"
	"sync"
)

// LaunchRequest supplies all command inputs. Nil Cwd inherits the caller's
// working directory, and nil Env inherits the caller's environment.
type LaunchRequest struct {
	Argv   []string
	Cwd    *string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Owner owns one started process tree until Close returns.
type Owner struct {
	process   processTree
	done      chan struct{}
	waitErr   error
	waitOnce  sync.Once
	termOnce  sync.Once
	termErr   error
	closeOnce sync.Once
	closeErr  error
}

// Launch starts a new owned process tree.
func Launch(request LaunchRequest) (*Owner, error) {
	if len(request.Argv) == 0 {
		return nil, errors.New("owned process launch requires argv")
	}
	cmd := exec.Command(request.Argv[0], request.Argv[1:]...)
	if request.Cwd != nil {
		cmd.Dir = *request.Cwd
	}
	if request.Env != nil {
		cmd.Env = append([]string(nil), request.Env...)
	}
	cmd.Stdin = request.Stdin
	cmd.Stdout = request.Stdout
	cmd.Stderr = request.Stderr
	process, err := startProcessTree(cmd)
	if err != nil {
		return nil, err
	}
	owner := &Owner{
		process: process,
		done:    make(chan struct{}),
	}
	go owner.reap()
	return owner, nil
}

// Wait waits for the root process to exit and returns its exit status.
func (owner *Owner) Wait() error {
	if owner == nil {
		return nil
	}
	<-owner.done
	return owner.waitErr
}

// Terminate requests termination of the owned process tree.
func (owner *Owner) Terminate() error {
	if owner == nil {
		return nil
	}
	owner.termOnce.Do(func() {
		owner.termErr = owner.process.Terminate()
	})
	return owner.termErr
}

// Close forcefully removes the owned process tree and joins the root reaper.
// It is idempotent.
func (owner *Owner) Close() error {
	if owner == nil {
		return nil
	}
	owner.closeOnce.Do(func() {
		terminateErr := owner.Terminate()
		killErr := owner.process.Kill()
		_ = owner.Wait()
		owner.closeErr = errors.Join(terminateErr, killErr, owner.process.Close())
	})
	return owner.closeErr
}

func (owner *Owner) reap() {
	owner.waitOnce.Do(func() {
		owner.waitErr = owner.process.Wait()
		close(owner.done)
	})
}

type processTree interface {
	Wait() error
	Terminate() error
	Kill() error
	Close() error
}
