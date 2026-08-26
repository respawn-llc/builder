package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/worktreecontract"
)

type worktreeStepBoundaryClient struct {
	mu             sync.Mutex
	requests       []llm.Request
	firstToolInput json.RawMessage
}

type worktreeStepBoundaryObserver struct {
	queue func() error
	once  sync.Once
	err   error
}

func (o *worktreeStepBoundaryObserver) ObservePersistedStore(
	_ context.Context,
	snapshot session.PersistedStoreSnapshot,
) error {
	if snapshot.Meta.WorktreeReminder == nil || o.queue == nil {
		return nil
	}
	o.once.Do(func() {
		o.err = o.queue()
	})
	return o.err
}

func (c *worktreeStepBoundaryClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, llm.Request{Items: llm.CloneResponseItems(request.Items)})
	call := len(c.requests)
	c.mu.Unlock()
	switch call {
	case 1:
		return worktreeStepBoundaryToolResponse("wait-for-worktree", c.firstToolInput), nil
	case 2:
		return worktreeStepBoundaryToolResponse(
			"pwd-after-worktree",
			json.RawMessage(`{"cmd":"pwd","shell":"/bin/sh","login":false}`),
		), nil
	default:
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	}
}

func (c *worktreeStepBoundaryClient) snapshot() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]llm.Request, len(c.requests))
	for index := range c.requests {
		requests[index].Items = llm.CloneResponseItems(c.requests[index].Items)
	}
	return requests
}

func (*worktreeStepBoundaryClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func worktreeStepBoundaryToolResponse(callID string, input json.RawMessage) llm.Response {
	call := llm.ToolCall{
		ID:    callID,
		Name:  string(toolspec.ToolExecCommand),
		Input: input,
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:      llm.RoleAssistant,
			Content:   textutil.Value("working"),
			Phase:     textutil.Value(llm.MessagePhaseCommentary),
			ToolCalls: []llm.ToolCall{call},
		},
		ToolCalls: []llm.ToolCall{call},
		OutputItems: []llm.ResponseItem{{
			Type:   llm.ResponseItemTypeFunctionCall,
			ID:     textutil.Value(call.ID),
			CallID: textutil.Value(call.ID),
			Name:   textutil.Value(call.Name),
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}
}

func TestScheduledEnterRebindsActiveRuntimeAtNextAgentStepBoundary(t *testing.T) {
	env := newServiceTestEnv(t)
	target := mustCreateWorktree(t, env, "feature/step-boundary-enter")

	observer := &worktreeStepBoundaryObserver{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: env.cfg.PersistenceRoot,
		StoreOptions: append(
			env.store.AuthoritativeSessionStoreOptions(),
			session.WithPersistenceObserver(observer),
		),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close step-boundary authority: %v", err)
		}
	})
	env.service.authority = authority

	firstStarted := filepath.Join(t.TempDir(), "first-started")
	releaseFirst := filepath.Join(t.TempDir(), "release-first")
	firstCommand := fmt.Sprintf(
		"touch %q; while [ ! -f %q ]; do sleep 0.01; done",
		firstStarted,
		releaseFirst,
	)
	firstInput, err := json.Marshal(map[string]any{
		"cmd":   firstCommand,
		"shell": "/bin/sh",
		"login": false,
	})
	if err != nil {
		t.Fatalf("marshal first tool input: %v", err)
	}
	client := &worktreeStepBoundaryClient{firstToolInput: firstInput}
	settings := env.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:              settings,
		EnabledTools:          []toolspec.ID{toolspec.ToolExecCommand},
		QuestionsEnabled:      textutil.Value(runtime.DefaultQuestionsEnabled),
		AutoCompactionEnabled: textutil.Value(runtime.DefaultAutoCompactionEnabled),
		FilesystemContext: func() tools.FilesystemContext {
			filesystemContext, contextErr := runtimewire.NewFilesystemContext(
				env.workspaceRoot,
				env.workspaceRoot,
				metadata.ProjectWorkspaceBoundary{ProjectID: env.binding.ProjectID},
			)
			if contextErr != nil {
				t.Fatalf("new filesystem context: %v", contextErr)
			}
			return filesystemContext
		}(),
		Client: client,
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	attachment, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "step-boundary-test",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	observer.queue = func() error {
		return authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
			_, accepted, queueErr := engine.QueueUserMessageForActiveRun(
				context.Background(),
				"steer after worktree switch",
				runtimeids.NewRuntimeClientRequestID(),
				nil,
			)
			if queueErr != nil {
				return queueErr
			}
			if !accepted {
				return errors.New("queued steer was not accepted")
			}
			return nil
		})
	}

	submitDone := make(chan error, 1)
	go func() {
		submitDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(ctx context.Context, engine *runtime.Engine) error {
			_, submitErr := engine.SubmitUserMessage(ctx, "start")
			return submitErr
		})
	}()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, statErr := os.Stat(firstStarted)
		return statErr == nil
	}, "timed out waiting for first tool")

	if _, err := env.service.EnterWorktree(context.Background(), worktreecontract.EnterRequest{
		TransitionHeader: worktreecontract.TransitionHeader{
			OperationID: worktreecontract.NewOperationID(),
			SessionID:   env.session.Meta().SessionID,
		},
		Selector: target.WorktreeID,
	}); err != nil {
		t.Fatalf("schedule EnterWorktree: %v", err)
	}
	// The transition scheduler is asynchronous. Keep the current tool open long
	// enough for its boundary reservation to enter the runtime's FIFO.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(releaseFirst, []byte("release"), 0o600); err != nil {
		t.Fatalf("release first tool: %v", err)
	}
	if outcome := waitForDeleteActivityTransitionOutcome(t, env.publisher); outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("worktree transition outcome = %+v, want completed", outcome)
	}
	if err := <-submitDone; err != nil {
		t.Fatalf("submit active turn: %v", err)
	}

	assertServiceTestSessionTarget(t, env, target.WorktreeID, target.CanonicalRoot)
	requests := client.snapshot()
	if len(requests) != 3 {
		t.Fatalf("model request count = %d, want 3", len(requests))
	}
	secondMessages := llm.MessagesFromItems(requests[1].Items)
	var reminderFound, steerFound bool
	for _, message := range secondMessages {
		if message.MessageType != nil &&
			*message.MessageType == llm.MessageTypeWorktreeMode &&
			message.WorktreeContext != nil &&
			message.WorktreeContext.EffectiveCwd == target.CanonicalRoot {
			reminderFound = true
		}
		if message.Role == llm.RoleUser &&
			message.Content != nil &&
			*message.Content == "steer after worktree switch" {
			steerFound = true
		}
	}
	if !reminderFound || !steerFound {
		t.Fatalf("next Agent Step reminder=%t steer=%t messages=%+v", reminderFound, steerFound, secondMessages)
	}
	var pwd string
	for _, item := range requests[2].Items {
		if item.Type != llm.ResponseItemTypeFunctionCallOutput ||
			item.CallID == nil ||
			*item.CallID != "pwd-after-worktree" {
			continue
		}
		if err := json.Unmarshal(item.Output, &pwd); err != nil {
			t.Fatalf("decode pwd tool output: %v", err)
		}
		pwd = strings.TrimSpace(pwd)
	}
	if pwd != target.CanonicalRoot {
		t.Fatalf("pwd after transition = %q, want %q", pwd, target.CanonicalRoot)
	}
	if err := authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *runtime.Engine) error {
		reminder := engine.WorktreeReminderState()
		if reminder == nil || reminder.EffectiveCwd != target.CanonicalRoot {
			t.Fatalf("runtime reminder = %+v, want effective cwd %q", reminder, target.CanonicalRoot)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect runtime after transition: %v", err)
	}
}

func TestEnterWorktreeRejectsInvalidSelectorsBeforeScheduling(t *testing.T) {
	env := newServiceTestEnv(t)
	validRoot := createExternalWorktree(t, env, "feature/valid-after-invalid")
	ambiguousRoot := filepath.Join(t.TempDir(), filepath.Base(validRoot))
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "feature/ambiguous-enter", ambiguousRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", ambiguousRoot) })

	for _, testCase := range []struct {
		selector string
		kind     worktreecontract.SelectorErrorKind
	}{
		{selector: "missing-worktree", kind: worktreecontract.SelectorErrorKindNotFound},
		{selector: filepath.Base(validRoot), kind: worktreecontract.SelectorErrorKindAmbiguous},
	} {
		_, err := env.service.EnterWorktree(env.ctx, worktreecontract.EnterRequest{
			TransitionHeader: worktreecontract.TransitionHeader{
				OperationID: worktreecontract.NewOperationID(),
				SessionID:   env.session.Meta().SessionID,
			},
			Selector: testCase.selector,
		})
		var selectorErr *worktreecontract.SelectorError
		if !errors.As(err, &selectorErr) || selectorErr.Kind != testCase.kind {
			t.Fatalf("selector %q error = %v, want %s", testCase.selector, err, testCase.kind)
		}
	}
}

func TestModelStepEnterRejectsInactiveExactExecution(t *testing.T) {
	env := newServiceTestEnv(t)
	createExternalWorktree(t, env, "feature/model-step-enter")
	ack, err := env.service.EnterWorktree(env.ctx, worktreecontract.EnterRequest{
		TransitionHeader: worktreecontract.TransitionHeader{
			OperationID: worktreecontract.NewOperationID(),
			SessionID:   env.session.Meta().SessionID,
			Origin: &worktreecontract.RuntimeStepOrigin{
				RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
				StepID: "018fdd67-89ab-4cde-8123-456789abc002",
			},
		},
		Selector: "feature/model-step-enter",
	})
	var immediate *worktreecontract.ImmediateTransitionError
	if !errors.As(err, &immediate) || immediate.Kind != worktreecontract.ImmediateTransitionOriginInactive ||
		ack != (worktreecontract.ScheduledAcknowledgement{}) {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestModelStepLeaveAndCurrentDeleteRejectInactiveExactExecution(t *testing.T) {
	origin := &worktreecontract.RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: "018fdd67-89ab-4cde-8123-456789abc002",
	}
	for _, operation := range []struct {
		name string
		run  func(*serviceTestEnv) error
	}{
		{
			name: "leave",
			run: func(env *serviceTestEnv) error {
				_, err := env.service.LeaveWorktree(env.ctx, worktreecontract.LeaveRequest{
					TransitionHeader: worktreecontract.TransitionHeader{
						OperationID: worktreecontract.NewOperationID(),
						SessionID:   env.session.Meta().SessionID,
						Origin:      origin,
					},
				})
				return err
			},
		},
		{
			name: "delete_current",
			run: func(env *serviceTestEnv) error {
				created := mustCreateWorktree(t, env, "feature/model-step-delete")
				updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
				_, err := env.service.DeleteWorktree(env.ctx, worktreecontract.DeleteRequest{
					TransitionHeader: worktreecontract.TransitionHeader{
						OperationID: worktreecontract.NewOperationID(),
						SessionID:   env.session.Meta().SessionID,
						Origin:      origin,
					},
					Selector:            created.WorktreeID,
					BranchCleanupPolicy: worktreecontract.BranchCleanupModeRetain,
				})
				return err
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run(newServiceTestEnv(t))
			var immediate *worktreecontract.ImmediateTransitionError
			if !errors.As(err, &immediate) || immediate.Kind != worktreecontract.ImmediateTransitionOriginInactive {
				t.Fatalf("error=%v, want inactive model-step origin", err)
			}
		})
	}
}

func createExternalWorktree(t *testing.T, env *serviceTestEnv, branch string) string {
	t.Helper()
	root := env.baseDir + "-external"
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", branch, root, "HEAD")
	t.Cleanup(func() {
		if _, err := os.Stat(root); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "remove", "--force", root)
		}
	})
	return root
}
