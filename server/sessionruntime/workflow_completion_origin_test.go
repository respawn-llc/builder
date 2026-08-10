package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestWorkflowCompletionOriginAuthorityRejectsStaleAndFinalizesCommittedAgent(t *testing.T) {
	fixture, resource, release := worktreeBoundaryArbitrationFixture(t)
	defer release()
	origin := serverapi.RuntimeStepOrigin{
		RunID:  uuid.NewString(),
		StepID: uuid.NewString(),
	}
	scopeID, err := resource.AgentStepBegan(context.Background(), origin)
	if err != nil {
		t.Fatalf("AgentStepBegan: %v", err)
	}

	mutations := 0
	stale := serverapi.RuntimeStepOrigin{RunID: origin.RunID, StepID: uuid.NewString()}
	if _, err := fixture.authority.ApplyWorkflowCompletion(scopeID, stale, func() (bool, error) {
		mutations++
		return true, nil
	}); !errors.Is(err, ErrExecutionNoLongerLive) {
		t.Fatalf("stale completion error = %v, want no-longer-live", err)
	}
	if mutations != 0 {
		t.Fatalf("stale completion mutations = %d, want zero", mutations)
	}

	transactionErr := errors.New("transaction failed")
	if committed, err := fixture.authority.ApplyWorkflowCompletion(scopeID, origin, func() (bool, error) {
		mutations++
		return false, transactionErr
	}); committed || !errors.Is(err, transactionErr) {
		t.Fatalf("failed transaction = committed:%t error:%v", committed, err)
	}
	if _, live := fixture.authority.SessionExecution(resource.ref.SessionID()); !live {
		t.Fatal("failed transaction removed the live exact scope")
	}

	if committed, err := fixture.authority.ApplyWorkflowCompletion(scopeID, origin, func() (bool, error) {
		mutations++
		return true, nil
	}); !committed || err != nil {
		t.Fatalf("committed completion = committed:%t error:%v", committed, err)
	}
	if mutations != 2 {
		t.Fatalf("Workflow mutations = %d, want failed then committed", mutations)
	}
	if _, live := fixture.authority.SessionExecution(resource.ref.SessionID()); live {
		t.Fatal("finalizing Agent remained visible as a live Session execution")
	}
	if _, retained := fixture.authority.ExecutionByScope(scopeID); !retained {
		t.Fatal("finalizing Agent scope was not retained for its natural owner")
	}
}
