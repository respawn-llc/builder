//go:build darwin

package sleepguard

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

type platformGuardImpl struct {
	cmd  *exec.Cmd
	done <-chan struct{}
}

func newPlatformGuardImpl() platformGuard {
	return &platformGuardImpl{}
}

func (p *platformGuardImpl) start() error {
	// -i: prevent idle sleep only; display sleep is unaffected
	// -w: bind the inhibitor lifetime to the owning Kent process
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("caffeinate start: %w", err)
	}
	done := make(chan struct{})
	p.cmd = cmd
	p.done = done
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return nil
}

func (p *platformGuardImpl) stop() {
	if p.cmd == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	if p.done != nil {
		<-p.done
	}
	p.cmd = nil
	p.done = nil
}

func (p *platformGuardImpl) running() bool {
	if p.cmd == nil || p.done == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *platformGuardImpl) exited() <-chan struct{} {
	return p.done
}
