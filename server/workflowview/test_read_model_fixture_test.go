package workflowview

import (
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

type workflowViewTestFixture struct {
	metadata     *metadata.Store
	definitions  *DefinitionProjection
	projector    *TaskProjector
	roleResolver workflow.RoleResolver
	transcripts  SessionActiveTranscriptProvider
	prompts      PendingPromptSource
	authority    *sessionruntime.Authority
	boardModule  *Board
	taskList     *TaskList
	taskDetail   *TaskDetail
	activity     *Activity
	attention    *Attention
}

func newWorkflowViewTestFixture(metadataStore *metadata.Store, workflowStore *workflowstore.Store, transcripts SessionActiveTranscriptProvider, prompts PendingPromptSource) (*workflowViewTestFixture, error) {
	if metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	if workflowStore == nil {
		return nil, errors.New("workflow store is required")
	}
	roleResolver := testsetup.QuestionsEnabled("coder")
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		return nil, err
	}
	return &workflowViewTestFixture{
		metadata:     metadataStore,
		definitions:  definitions,
		projector:    NewTaskProjector(),
		roleResolver: roleResolver,
		transcripts:  transcripts,
		prompts:      prompts,
		authority: sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
			PersistenceRoot: metadataStore.PersistenceRoot(),
			StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
		}),
	}, nil
}

func (f *workflowViewTestFixture) board(t *testing.T) *Board {
	t.Helper()
	if f.boardModule == nil {
		var err error
		f.boardModule, err = NewBoard(f.metadata, f.definitions, f.roleResolver, f.projector, f.authority)
		if err != nil {
			t.Fatalf("NewBoard: %v", err)
		}
	}
	return f.boardModule
}

func (f *workflowViewTestFixture) tasks(t *testing.T) *TaskList {
	t.Helper()
	if f.taskList == nil {
		var err error
		f.taskList, err = NewTaskList(f.metadata, f.definitions, f.projector, f.authority)
		if err != nil {
			t.Fatalf("NewTaskList: %v", err)
		}
	}
	return f.taskList
}

func (f *workflowViewTestFixture) detail(t *testing.T) *TaskDetail {
	t.Helper()
	if f.taskDetail == nil {
		var err error
		f.taskDetail, err = NewTaskDetail(f.metadata, f.projector, f.authority)
		if err != nil {
			t.Fatalf("NewTaskDetail: %v", err)
		}
	}
	return f.taskDetail
}

func (f *workflowViewTestFixture) taskActivity(t *testing.T) *Activity {
	t.Helper()
	if f.activity == nil {
		var err error
		f.activity, err = NewActivity(f.metadata, f.definitions, f.projector)
		if err != nil {
			t.Fatalf("NewActivity: %v", err)
		}
	}
	return f.activity
}

func (f *workflowViewTestFixture) taskAttention(t *testing.T) *Attention {
	t.Helper()
	if f.attention == nil {
		var err error
		f.attention, err = NewAttention(f.metadata.Queries(), f.projector, f.transcripts, f.prompts)
		if err != nil {
			t.Fatalf("NewAttention: %v", err)
		}
	}
	return f.attention
}
