package runtimecommand

import (
	"errors"
	"sync"
)

var (
	ErrCompletionFenced     = errors.New("runtime command completion fence is closed")
	ErrCompletionSuperseded = errors.New("runtime command completion attempt was superseded")
)

type CompletionFence struct {
	mu         sync.Mutex
	target     Target
	generation uint64
	nextLease  uint64
	fenced     bool
	reserved   uint64
}

type CompletionAttempt struct {
	fence      *CompletionFence
	generation uint64
}

type CompletionLease struct {
	fence      *CompletionFence
	generation uint64
	leaseID    uint64
	mu         sync.Mutex
	terminal   bool
}

type InputAcceptance struct {
	fence      *CompletionFence
	generation uint64
	previous   uint64
	mu         sync.Mutex
	terminal   bool
}

func NewCompletionFence(target Target) *CompletionFence {
	return &CompletionFence{target: target, generation: 1}
}

func (f *CompletionFence) Begin() (CompletionAttempt, error) {
	if f == nil {
		return CompletionAttempt{}, errors.New("runtime command completion fence is required")
	}
	if err := f.target.Validate(); err != nil {
		return CompletionAttempt{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fenced || f.reserved != 0 {
		return CompletionAttempt{}, ErrCompletionFenced
	}
	return CompletionAttempt{fence: f, generation: f.generation}, nil
}

func (f *CompletionFence) AcceptInput() (uint64, error) {
	acceptance, err := f.BeginInput()
	if err != nil {
		return 0, err
	}
	if err := acceptance.Commit(); err != nil {
		return 0, err
	}
	return acceptance.generation, nil
}

func (f *CompletionFence) BeginInput() (InputAcceptance, error) {
	if f == nil {
		return InputAcceptance{}, errors.New("runtime command completion fence is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fenced || f.reserved != 0 {
		return InputAcceptance{}, ErrCompletionFenced
	}
	previous := f.generation
	f.generation++
	return InputAcceptance{fence: f, previous: previous, generation: f.generation}, nil
}

func (a *InputAcceptance) Commit() error {
	if a == nil || a.fence == nil {
		return errors.New("runtime command input acceptance is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminal {
		return errors.New("runtime command input acceptance is already terminal")
	}
	a.terminal = true
	return nil
}

func (a *InputAcceptance) Abort() error {
	if a == nil || a.fence == nil {
		return errors.New("runtime command input acceptance is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminal {
		return errors.New("runtime command input acceptance is already terminal")
	}
	a.fence.mu.Lock()
	defer a.fence.mu.Unlock()
	if a.fence.generation != a.generation {
		return ErrCompletionSuperseded
	}
	a.fence.generation = a.previous
	a.terminal = true
	return nil
}

func (a CompletionAttempt) Acquire() (CompletionLease, error) {
	if a.fence == nil {
		return CompletionLease{}, errors.New("runtime command completion attempt is required")
	}
	a.fence.mu.Lock()
	defer a.fence.mu.Unlock()
	if a.fence.fenced || a.fence.reserved != 0 {
		return CompletionLease{}, ErrCompletionFenced
	}
	if a.fence.generation != a.generation {
		return CompletionLease{}, ErrCompletionSuperseded
	}
	a.fence.nextLease++
	if a.fence.nextLease == 0 {
		panic("runtime command completion lease ID overflow")
	}
	return CompletionLease{
		fence:      a.fence,
		generation: a.generation,
		leaseID:    a.fence.nextLease,
	}, nil
}

func (l *CompletionLease) Reserve() error {
	if l == nil || l.fence == nil {
		return errors.New("runtime command completion lease is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminal {
		return errors.New("runtime command completion lease is already terminal")
	}
	l.fence.mu.Lock()
	defer l.fence.mu.Unlock()
	if l.fence.generation != l.generation {
		return ErrCompletionSuperseded
	}
	if l.fence.reserved != 0 && l.fence.reserved != l.leaseID {
		return ErrCompletionFenced
	}
	l.fence.reserved = l.leaseID
	return nil
}

func (l *CompletionLease) Commit() error {
	if l == nil || l.fence == nil {
		return errors.New("runtime command completion lease is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminal {
		return errors.New("runtime command completion lease is already terminal")
	}
	l.fence.mu.Lock()
	defer l.fence.mu.Unlock()
	if l.fence.generation != l.generation {
		return ErrCompletionSuperseded
	}
	if l.fence.reserved != 0 && l.fence.reserved != l.leaseID {
		return ErrCompletionFenced
	}
	l.fence.fenced = true
	l.fence.reserved = 0
	l.terminal = true
	return nil
}

func (l *CompletionLease) Abort() error {
	if l == nil || l.fence == nil {
		return errors.New("runtime command completion lease is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminal {
		return errors.New("runtime command completion lease is already terminal")
	}
	l.fence.mu.Lock()
	defer l.fence.mu.Unlock()
	if l.fence.generation != l.generation {
		return ErrCompletionSuperseded
	}
	if l.fence.reserved != 0 && l.fence.reserved != l.leaseID {
		return ErrCompletionFenced
	}
	if l.fence.reserved == l.leaseID {
		l.fence.reserved = 0
	}
	l.terminal = true
	return nil
}

// Reopen starts a new exact execution lifecycle after the prior command lease
// has been released. Open attempts from the old lifecycle become superseded.
func (f *CompletionFence) Reopen() error {
	if f == nil {
		return errors.New("runtime command completion fence is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserved != 0 {
		return errors.New("runtime command completion fence has a reserved lease")
	}
	f.generation++
	if f.generation == 0 {
		panic("runtime command completion generation overflow")
	}
	f.fenced = false
	return nil
}
