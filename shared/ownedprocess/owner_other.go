//go:build !(aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris || windows)

package ownedprocess

import (
	"os/exec"
	"sync"
)

type otherProcessTree struct {
	cmd       *exec.Cmd
	closeOnce sync.Once
	closeErr  error
}

func startProcessTree(cmd *exec.Cmd) (processTree, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &otherProcessTree{cmd: cmd}, nil
}

func (tree *otherProcessTree) Wait() error {
	return tree.cmd.Wait()
}

func (tree *otherProcessTree) Terminate() error {
	if tree.cmd.Process == nil {
		return nil
	}
	return tree.cmd.Process.Kill()
}

func (tree *otherProcessTree) Kill() error {
	return tree.Terminate()
}

func (tree *otherProcessTree) Close() error {
	tree.closeOnce.Do(func() {
		tree.closeErr = nil
	})
	return tree.closeErr
}
