package runtime

import (
	"fmt"
	"sync"

	"core/server/workflowruntime"
)

type workflowPromptDeliveryState struct {
	mu       sync.Mutex
	pending  bool
	delivery workflowruntime.TaskPromptDelivery
}

func newWorkflowPromptDeliveryState(execution *workflowruntime.CurrentNodeExecutionConfig) *workflowPromptDeliveryState {
	if execution == nil {
		return &workflowPromptDeliveryState{}
	}
	return &workflowPromptDeliveryState{
		pending:  true,
		delivery: execution.TaskPromptDelivery,
	}
}

func (s *workflowPromptDeliveryState) trigger(defaultTrigger workflowTaskPromptTrigger) workflowTaskPromptTrigger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.triggerLocked(defaultTrigger)
}

func (s *workflowPromptDeliveryState) apply(
	defaultTrigger workflowTaskPromptTrigger,
	run func(workflowTaskPromptTrigger) error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	trigger := s.triggerLocked(defaultTrigger)
	if err := run(trigger); err != nil {
		return err
	}
	s.pending = false
	return nil
}

func (s *workflowPromptDeliveryState) triggerLocked(defaultTrigger workflowTaskPromptTrigger) workflowTaskPromptTrigger {
	if s.pending {
		switch s.delivery {
		case workflowruntime.TaskPromptDeliveryAssignment:
			return workflowTaskPromptTriggerAssignmentDelivery
		case workflowruntime.TaskPromptDeliveryResume:
			if defaultTrigger == workflowTaskPromptTriggerTaskDelivery {
				return workflowTaskPromptTriggerResumeDelivery
			}
		default:
			panic(fmt.Sprintf("select workflow prompt delivery trigger: invalid task prompt delivery %d", s.delivery))
		}
	}
	return defaultTrigger
}
