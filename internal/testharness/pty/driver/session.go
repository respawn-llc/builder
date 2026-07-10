package driver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"core/internal/testharness/pty/analyzer"

	creackpty "github.com/creack/pty"
	"github.com/google/uuid"
)

const sessionCommandCapacity = 64

type SessionEventKind int

const (
	SessionEventTerminalAnalysis SessionEventKind = iota + 1
	SessionEventProcessExit
	SessionEventCommandStarted
	SessionEventCommandCompleted
	SessionEventCommandFailed
	SessionEventFailure
)

// SessionEvent is an immutable observation emitted by one PTY reactor.
type SessionEvent struct {
	Kind        SessionEventKind
	CommandID   uuid.UUID
	Analysis    *analyzer.Analysis
	ProcessExit *analyzer.ProcessExit
	Err         error
}

// Session starts an arbitrary executable in a PTY. It owns PTY reads, command
// ordering, and capture assembly but deliberately knows nothing about scenarios
// or model transports.
type Session struct {
	cmd      *exec.Cmd
	ptmx     *os.File
	commands chan SessionCommand
	events   chan SessionEvent
	done     chan struct{}

	mu      sync.Mutex
	capture analyzer.Capture
	err     error
	closed  bool
}

// StartSession starts a child with exactly SessionSpec.Env, not an ambient
// environment overlay.
func StartSession(spec SessionSpec) (*Session, error) {
	cmd, ptmx, err := sessionStart(spec)
	if err != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(int(ptmx.Fd()), true); err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("make PTY nonblocking: %w", err)
	}
	session := &Session{
		cmd:      cmd,
		ptmx:     ptmx,
		commands: make(chan SessionCommand, sessionCommandCapacity),
		events:   make(chan SessionEvent, 128),
		done:     make(chan struct{}),
	}
	go session.run(spec.Dimensions)
	return session, nil
}

func (s *Session) Events() <-chan SessionEvent {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *Session) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Enqueue never blocks the runner. Validation happens before the command
// becomes visible to the reactor.
func (s *Session) Enqueue(command SessionCommand) error {
	if s == nil {
		return errors.New("PTY session is required")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("PTY session is closed")
	}
	select {
	case s.commands <- copySessionCommand(command):
		return nil
	default:
		return errors.New("PTY session command queue is full")
	}
}

func (s *Session) Capture() (analyzer.Capture, error) {
	if s == nil {
		return analyzer.Capture{}, errors.New("PTY session is required")
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.capture, s.err
	}
	return s.capture, nil
}

// Close stops admission and closes PTY ingress. Process termination is a
// runner-owned lifecycle decision and remains an explicit command.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.ptmx.Close()
}

// Terminate sends SIGTERM to the child process group. It is intended solely
// for the harness lifecycle owner after command admission has stopped.
func (s *Session) Terminate() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Signal(syscall.SIGTERM)
}

// ForceKill sends SIGKILL to the child process group after the cleanup grace.
func (s *Session) ForceKill() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

func (s *Session) run(dimensions analyzer.Dimensions) {
	defer close(s.done)
	defer close(s.events)
	defer func() { _ = s.ptmx.Close() }()

	assembler, err := analyzer.NewCaptureAssembler(dimensions)
	if err != nil {
		s.fail(err)
		return
	}
	stream, err := analyzer.NewStream(dimensions)
	if err != nil {
		s.fail(err)
		return
	}
	wait := make(chan error, 1)
	go func() { wait <- s.cmd.Wait() }()

	buffer := make([]byte, 16*1024)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var waitErr error
	exited := false
	for !exited {
		if err := s.drainReadable(buffer, assembler, stream); err != nil {
			s.fail(err)
			return
		}
		select {
		case command := <-s.commands:
			s.emit(SessionEvent{Kind: SessionEventCommandStarted, CommandID: command.ID})
			if err := s.execute(command, assembler, stream, buffer); err != nil {
				s.emit(SessionEvent{Kind: SessionEventCommandFailed, CommandID: command.ID, Err: err})
			} else {
				s.emit(SessionEvent{Kind: SessionEventCommandCompleted, CommandID: command.ID})
			}
		case waitErr = <-wait:
			exited = true
		case <-ticker.C:
		}
	}
	if err := s.drainReadable(buffer, assembler, stream); err != nil {
		s.fail(err)
		return
	}
	capture, err := assembler.Capture()
	if err != nil {
		s.fail(err)
		return
	}
	capture.ProcessExit = processExit(s.cmd.ProcessState)
	capture.ReadLoopDone = true
	analysis, analysisErr := analyzer.Analyze(capture)
	if analysisErr != nil {
		s.fail(analysisErr)
		return
	}
	s.mu.Lock()
	s.capture = capture
	s.mu.Unlock()
	s.emit(SessionEvent{Kind: SessionEventTerminalAnalysis, Analysis: &analysis})
	s.emit(SessionEvent{Kind: SessionEventProcessExit, ProcessExit: capture.ProcessExit})
	_ = waitErr // Exit status is an immutable process observation, not a driver failure.
}

func (s *Session) execute(command SessionCommand, assembler *analyzer.CaptureAssembler, stream *analyzer.Stream, buffer []byte) error {
	switch command.Kind {
	case SessionCommandWrite, SessionCommandRuntimeControlByte:
		if _, err := s.ptmx.Write(command.Bytes); err != nil {
			return fmt.Errorf("write PTY command %s: %w", command.ID, err)
		}
		return nil
	case SessionCommandResize:
		if err := s.drainReadable(buffer, assembler, stream); err != nil {
			return err
		}
		if err := creackpty.Setsize(s.ptmx, &creackpty.Winsize{Rows: uint16(command.Dimensions.Rows), Cols: uint16(command.Dimensions.Cols)}); err != nil {
			return fmt.Errorf("resize PTY command %s: %w", command.ID, err)
		}
		if err := assembler.Resize(*command.Dimensions); err != nil {
			return err
		}
		if err := stream.Resize(*command.Dimensions); err != nil {
			return err
		}
		return nil
	case SessionCommandTerminateProcess:
		if s.cmd.Process == nil {
			return errors.New("PTY child process is unavailable")
		}
		return s.cmd.Process.Signal(syscall.SIGTERM)
	default:
		return fmt.Errorf("unsupported session command kind %d", command.Kind)
	}
}

func (s *Session) drainReadable(buffer []byte, assembler *analyzer.CaptureAssembler, stream *analyzer.Stream) error {
	for {
		count, err := s.ptmx.Read(buffer)
		if count > 0 {
			payload := append([]byte(nil), buffer[:count]...)
			if appendErr := assembler.Append(payload); appendErr != nil {
				return appendErr
			}
			if feedErr := stream.Feed(payload); feedErr != nil {
				return feedErr
			}
			analysis, snapshotErr := stream.Snapshot()
			if snapshotErr != nil {
				return snapshotErr
			}
			s.emit(SessionEvent{Kind: SessionEventTerminalAnalysis, Analysis: &analysis})
		}
		if err == nil {
			if count == 0 {
				return nil
			}
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
			return nil
		}
		return fmt.Errorf("read PTY: %w", err)
	}
}

func (s *Session) emit(event SessionEvent) {
	select {
	case s.events <- event:
	default:
		s.fail(errors.New("PTY session event buffer is full"))
	}
}

func (s *Session) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	select {
	case s.events <- SessionEvent{Kind: SessionEventFailure, Err: err}:
	default:
	}
}

func copySessionCommand(command SessionCommand) SessionCommand {
	copy := command
	copy.Bytes = append([]byte(nil), command.Bytes...)
	if command.Dimensions != nil {
		dimensions := *command.Dimensions
		copy.Dimensions = &dimensions
	}
	return copy
}

func sessionStart(spec SessionSpec) (*exec.Cmd, *os.File, error) {
	if _, err := analyzer.NewDimensions(spec.Dimensions.Rows, spec.Dimensions.Cols); err != nil {
		return nil, nil, err
	}
	if spec.Path == "" {
		return nil, nil, errors.New("session command path is required")
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = append([]string(nil), spec.Env...)
	cmd.Dir = spec.Dir
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Rows: uint16(spec.Dimensions.Rows),
		Cols: uint16(spec.Dimensions.Cols),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start PTY session path=%s args=%v: %w", spec.Path, spec.Args, err)
	}
	// StartWithSize installs dimensions before exec. Reapplying them after the
	// child exists delivers SIGWINCH so terminal clients render their initial
	// frame without relying on host-window resize timing.
	if err := creackpty.Setsize(ptmx, &creackpty.Winsize{
		Rows: uint16(spec.Dimensions.Rows),
		Cols: uint16(spec.Dimensions.Cols),
	}); err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("notify PTY child of initial dimensions: %w", err)
	}
	return cmd, ptmx, nil
}
