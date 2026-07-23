package workflowexecution

import (
	"errors"
	"sync"
)

// FatalSignal publishes the first unrecoverable Workflow Execution failure to
// the server lifecycle owner. The buffered channel keeps reporting non-blocking
// even when the serve loop is concurrently shutting down.
type FatalSignal struct {
	once     sync.Once
	failures chan error
}

func NewFatalSignal() *FatalSignal {
	return &FatalSignal{failures: make(chan error, 1)}
}

func (s *FatalSignal) Report(failure error) error {
	if s == nil {
		return errors.New("workflow execution fatal signal is required")
	}
	if failure == nil {
		return errors.New("workflow execution fatal failure is required")
	}
	s.once.Do(func() {
		s.failures <- failure
	})
	return nil
}

func (s *FatalSignal) Failures() <-chan error {
	if s == nil {
		return nil
	}
	return s.failures
}
