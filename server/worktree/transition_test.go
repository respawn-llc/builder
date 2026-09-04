package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/worktreecontract"

	"github.com/google/uuid"
)

type externalIndeterminateWorktreeError struct{ error }

func (*externalIndeterminateWorktreeError) WorktreeTransitionIndeterminate() {}

type emptySessionRetargetProcessSource struct{}

func (emptySessionRetargetProcessSource) List() []shelltool.Snapshot { return nil }

type scheduledSessionRetargeterStub struct {
	request    metadata.SessionWorkspaceRetargetRequest
	resolve    func(context.Context) (metadata.SessionWorkspaceRetargetRequest, error)
	origin     serverapi.RuntimeStepOrigin
	operation  worktreecontract.OperationID
	completion func(error)
}

func (*scheduledSessionRetargeterStub) RetargetWorkspace(
	context.Context,
	metadata.SessionWorkspaceRetargetRequest,
) (metadata.SessionWorkspaceRetargetResult, error) {
	return metadata.SessionWorkspaceRetargetResult{}, errors.New("unexpected synchronous Session retarget")
}

func (s *scheduledSessionRetargeterStub) ScheduleWorkspaceRetargetWithCompletion(
	_ context.Context,
	request metadata.SessionWorkspaceRetargetRequest,
	origin serverapi.RuntimeStepOrigin,
	operation worktreecontract.OperationID,
	completion func(error),
) (serverapi.SessionWorkspaceRetargetScheduledAcknowledgement, error) {
	s.request = request
	s.origin = origin
	s.operation = operation
	s.completion = completion
	return serverapi.SessionWorkspaceRetargetScheduledAcknowledgement{OperationID: operation}, nil
}

func (s *scheduledSessionRetargeterStub) ScheduleWorkspaceRetargetResolutionWithCompletion(
	_ context.Context,
	sessionID string,
	origin serverapi.RuntimeStepOrigin,
	operation worktreecontract.OperationID,
	resolve func(context.Context) (metadata.SessionWorkspaceRetargetRequest, error),
	completion func(error),
) (serverapi.SessionWorkspaceRetargetScheduledAcknowledgement, error) {
	s.request.SessionID = sessionID
	s.resolve = resolve
	s.origin = origin
	s.operation = operation
	s.completion = completion
	return serverapi.SessionWorkspaceRetargetScheduledAcknowledgement{OperationID: operation}, nil
}

func TestEnterWorktreeSchedulesCrossWorkspaceResolutionBeforeSelectorExists(t *testing.T) {
	env := newServiceTestEnv(t)
	sourceSession := createNonGitSourceSession(t, env)
	retargeter := &scheduledSessionRetargeterStub{}
	env.service.sessionRetargeter = retargeter
	operationID := clientui.NewWorktreeTransitionID()
	runID, stepID := uuid.NewString(), uuid.NewString()
	requestCtx, cancelRequest := context.WithCancel(t.Context())

	ack, err := env.service.EnterWorktree(requestCtx, &worktreepb.EnterRequest{
		OperationId: operationID.String(),
		SessionId:   sourceSession.Meta().SessionID,
		Selector:    "feature/created-after-acceptance",
		TargetWorkspace: &worktreepb.TransitionWorkspace{
			WorkspaceId:   env.binding.WorkspaceID,
			WorkspaceRoot: env.workspaceRoot,
		},
		Origin: &worktreepb.TransitionRuntimeStepOrigin{RunId: runID, StepId: stepID},
	})
	if err != nil {
		t.Fatalf("EnterWorktree before selector exists: %v", err)
	}
	if ack.GetOperationId() != operationID.String() {
		t.Fatalf("ack operation = %q, want %q", ack.GetOperationId(), operationID)
	}
	cancelRequest()
	next := mustCreateWorktree(t, env, "feature/created-after-acceptance")
	if retargeter.resolve == nil {
		t.Fatal("scheduled transition did not retain target resolution")
	}
	request, err := retargeter.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve scheduled target after caller cancellation: %v", err)
	}
	if request.SessionID != sourceSession.Meta().SessionID ||
		request.TargetWorktreeID == nil ||
		*request.TargetWorktreeID != next.WorktreeID {
		t.Fatalf("resolved Session retarget = %+v, want Worktree %q", request, next.WorktreeID)
	}
}

func TestEnterWorktreeSchedulesCrossProjectRetargetFromNonGitWorkspace(t *testing.T) {
	env := newServiceTestEnv(t)
	next := mustCreateWorktree(t, env, "feature/scheduled-cross-project-enter")
	sourceSession := createNonGitSourceSession(t, env)
	retargeter := &scheduledSessionRetargeterStub{}
	env.service.sessionRetargeter = retargeter
	operationID := clientui.NewWorktreeTransitionID()
	runID, stepID := uuid.NewString(), uuid.NewString()

	ack, err := env.service.EnterWorktree(t.Context(), &worktreepb.EnterRequest{
		OperationId: operationID.String(),
		SessionId:   sourceSession.Meta().SessionID,
		Selector:    next.DisplayName,
		TargetWorkspace: &worktreepb.TransitionWorkspace{
			WorkspaceId:   env.binding.WorkspaceID,
			WorkspaceRoot: env.workspaceRoot,
		},
		Origin: &worktreepb.TransitionRuntimeStepOrigin{RunId: runID, StepId: stepID},
	})
	if err != nil {
		t.Fatalf("EnterWorktree: %v", err)
	}
	if ack.GetOperationId() != operationID.String() {
		t.Fatalf("ack operation = %q, want %q", ack.GetOperationId(), operationID)
	}
	if retargeter.resolve == nil {
		t.Fatal("scheduled transition did not retain target resolution")
	}
	retargeter.request, err = retargeter.resolve(t.Context())
	if err != nil {
		t.Fatalf("resolve scheduled Session retarget: %v", err)
	}
	if retargeter.request.SessionID != sourceSession.Meta().SessionID ||
		retargeter.request.WorkspaceRoot != env.workspaceRoot ||
		retargeter.request.ProjectID == nil ||
		*retargeter.request.ProjectID != env.binding.ProjectID ||
		retargeter.request.TargetWorktreeID == nil ||
		*retargeter.request.TargetWorktreeID != next.WorktreeID {
		t.Fatalf("scheduled Session retarget = %+v", retargeter.request)
	}
	if retargeter.origin.RunID != runID ||
		retargeter.origin.StepID != stepID ||
		retargeter.operation.String() != operationID.String() ||
		retargeter.completion == nil {
		t.Fatalf("scheduled transition context = origin:%+v operation:%s completion:%t", retargeter.origin, retargeter.operation.String(), retargeter.completion != nil)
	}
	retargeter.completion(nil)
	if len(env.publisher.outcomes) != 1 || env.publisher.outcomes[0].State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("transition outcomes = %+v, want one completed outcome", env.publisher.outcomes)
	}
}

func TestEnterWorktreeMovesDormantSessionFromNonGitWorkspaceAcrossProjects(t *testing.T) {
	env := newServiceTestEnv(t)
	next := mustCreateWorktree(t, env, "feature/cross-project-enter")
	sourceSession := createNonGitSourceSession(t, env)
	env.service.sessionRetargeter = sessionservice.NewSessionWorkspaceRetargeter(
		env.store,
		env.authority,
		env.publisher,
		emptySessionRetargetProcessSource{},
	)
	operationID := clientui.NewWorktreeTransitionID()

	ack, err := env.service.EnterWorktree(t.Context(), &worktreepb.EnterRequest{
		OperationId: operationID.String(),
		SessionId:   sourceSession.Meta().SessionID,
		Selector:    next.DisplayName,
		TargetWorkspace: &worktreepb.TransitionWorkspace{
			WorkspaceId:   env.binding.WorkspaceID,
			WorkspaceRoot: env.workspaceRoot,
		},
	})
	if err != nil {
		t.Fatalf("EnterWorktree: %v", err)
	}
	if ack.GetOperationId() != operationID.String() {
		t.Fatalf("ack operation = %q, want %q", ack.GetOperationId(), operationID)
	}
	target, err := env.store.ResolveSessionExecutionTarget(t.Context(), sourceSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != env.binding.WorkspaceID || sessionTargetWorktreeID(target) != next.WorktreeID {
		t.Fatalf("retargeted execution target = %+v, want workspace %q worktree %q", target, env.binding.WorkspaceID, next.WorktreeID)
	}
	if target.EffectiveWorkdir != next.CanonicalRoot {
		t.Fatalf("effective workdir = %q, want %q", target.EffectiveWorkdir, next.CanonicalRoot)
	}
	belongs, err := env.store.SessionBelongsToProject(t.Context(), sourceSession.Meta().SessionID, env.binding.ProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject: %v", err)
	}
	if !belongs {
		t.Fatal("session did not move to the target project")
	}
	reopened, err := session.OpenByID(
		env.cfg.PersistenceRoot,
		sourceSession.Meta().SessionID,
		env.store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID retargeted session: %v", err)
	}
	if reopened.Meta().WorktreeReminder == nil ||
		reopened.Meta().WorktreeReminder.WorktreePath != next.CanonicalRoot {
		t.Fatalf("persisted worktree reminder = %+v, want target %q", reopened.Meta().WorktreeReminder, next.CanonicalRoot)
	}
	if len(env.publisher.outcomes) != 1 || env.publisher.outcomes[0].State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("transition outcomes = %+v, want one completed outcome", env.publisher.outcomes)
	}
}

func createNonGitSourceSession(t *testing.T, env *serviceTestEnv) *session.Store {
	t.Helper()
	sourceRoot := t.TempDir()
	sourceBinding, err := env.store.CreateProjectForWorkspace(t.Context(), sourceRoot, "Non Git Source")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace source: %v", err)
	}
	sourceSessionsDir := filepath.Join(env.cfg.PersistenceRoot, "projects", sourceBinding.ProjectID, "sessions")
	sourceSession, err := session.Create(
		sourceSessionsDir,
		filepath.Base(sourceSessionsDir),
		sourceRoot,
		sessioncontract.SessionCategoryMain,
		env.store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create source: %v", err)
	}
	if err := sourceSession.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable source: %v", err)
	}
	return sourceSession
}

func TestWorktreeTransitionTerminalCases(t *testing.T) {
	writeFailure, syncFailure, publicationFailure, rollbackFailure := errors.New("write target"), errors.New("synchronize target"), errors.New("publish Session identity"), errors.New("rollback target")
	selectorFailure, publicationDiagnostic, rollbackDiagnostic := worktreecontract.NewSelectorError(worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND, "missing-worktree", nil), errors.New("publish session identity: "+publicationFailure.Error()), errors.Join(syncFailure, rollbackFailure)
	failed, completed := clientui.WorktreeTransitionFailed, clientui.WorktreeTransitionCompleted
	tests := []struct {
		name                                                      string
		dormant, selector                                         bool
		write, finish, rollback, diagnostic, publication, surface error
		outcome                                                   *clientui.WorktreeTransitionState
	}{
		{name: "pre-write technical failure", write: writeFailure, outcome: &failed, diagnostic: writeFailure},
		{name: "successful rollback after target-sync failure", finish: syncFailure, outcome: &failed, diagnostic: syncFailure},
		{name: "selector user-correctable failure", selector: true, outcome: &failed, diagnostic: selectorFailure},
		{name: "dormant selector user-correctable failure", dormant: true, selector: true, outcome: &failed, diagnostic: selectorFailure},
		{name: "applied target then identity publication failure", outcome: &completed, publication: publicationFailure, surface: publicationDiagnostic},
		{name: "active rollback failure", finish: syncFailure, rollback: rollbackFailure, surface: rollbackDiagnostic},
		{name: "runtime target rollback failure", finish: &externalIndeterminateWorktreeError{syncFailure}, rollback: worktreeApplied(rollbackFailure), surface: rollbackDiagnostic},
		{name: "dormant rollback failure", dormant: true, finish: syncFailure, rollback: rollbackFailure, diagnostic: rollbackDiagnostic},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newServiceTestEnv(t)
			operationID := clientui.NewWorktreeTransitionID()
			next := mustCreateWorktree(t, env, "feature/terminal-"+operationID.String())
			previous := mustResolveServiceTestTarget(t, env)
			restored, unavailable := make(chan runtimeinput.PendingWorkTechnicalRestoration, 1), make(chan string, 1)
			observe := func(event runtime.Event) {
				if event.PendingWorkRestoration != nil {
					restored <- *event.PendingWorkRestoration
				}
				if status := event.QueuedUserMessageStatus; status != nil && status.Status == runtime.QueuedUserMessageFailed &&
					status.FailureReason == runtime.QueuedUserMessageFailureRuntimeUnavailable {
					unavailable <- status.QueueItemID
				}
			}
			var attachment sessionruntime.RuntimeAttachment
			var engine *runtime.Engine
			var release func()
			if !test.dormant {
				plan := deleteActivityRuntimePlan(t, env, env.workspaceRoot, deleteActivityTestLLMClient{}, "off", nil, observe)
				var err error
				attachment, err = env.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{SessionID: openDeleteActivitySessionDescriptor(t, env.session.Meta().SessionID).SessionID(), OwnerID: "worktree-terminal-table", Runtime: &plan})
				requireTerminal(t, err == nil, "open Runtime: %v", err)
				err = env.authority.WithRuntime(t.Context(), attachment.Resource(), func(_ context.Context, current *runtime.Engine) error { engine = current; return nil })
				requireTerminal(t, err == nil, "capture Runtime: %v", err)
				started, unblock, done := make(chan struct{}, 1), make(chan struct{}), make(chan error, 1)
				go func() {
					done <- engine.RunWhenIdle(t.Context(), runtime.ActiveKindRuntimeMaintenance, func() error { started <- struct{}{}; <-unblock; return nil })
				}()
				_ = waitTerminal(t, started, "Runtime maintenance")
				release = func() { close(unblock); requireTerminal(t, <-done == nil, "release Runtime maintenance") }
			}
			env.publisher.identityErr = test.publication
			request := worktreeTransitionRequest{operationID: operationID, sessionID: env.session.Meta().SessionID, kind: clientui.WorktreeTransitionEnter, selector: next.WorktreeID}
			var ack *worktreepb.ScheduledAcknowledgement
			var runErr error
			if test.selector {
				ack, runErr = env.service.EnterWorktree(t.Context(), &worktreepb.EnterRequest{
					OperationId: operationID.String(),
					SessionId:   request.sessionID,
					Selector:    "missing-worktree",
					TargetWorkspace: &worktreepb.TransitionWorkspace{
						WorkspaceId:   env.binding.WorkspaceID,
						WorkspaceRoot: env.workspaceRoot,
					},
				})
			} else {
				ack, runErr = env.service.runWorktreeTransition(t.Context(), request,
					runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionEnter, Selector: &request.selector},
					func(ctx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
						apply := func(applyCtx context.Context) error {
							return runTerminalMutationCase(applyCtx, env, next.WorktreeID, previous, sync, test.write, test.finish, test.rollback, test.publication != nil)
						}
						return applyWorktreeTransition(ctx, authority, apply)
					})
			}
			if !test.dormant {
				requireTerminal(t, runErr == nil && ack.GetOperationId() == operationID.String(), "active acknowledgement = %+v, %v", ack, runErr)
				if test.rollback != nil {
					queued, err := engine.QueueUserMessage(t.Context(), "queued behind indeterminate transition")
					requireTerminal(t, err == nil, "queue human work: %v", err)
					release()
					failedID := waitTerminal(t, unavailable, "queued Runtime-unavailable failure")
					requireTerminal(t, failedID == queued.ID, "failed queued work = %s, want %s", failedID, queued.ID)
				} else {
					release()
					err := engine.RunWhenIdle(t.Context(), runtime.ActiveKindRuntimeMaintenance, func() error { return nil })
					requireTerminal(t, err == nil, "wait for terminal transition: %v", err)
				}
				if test.surface != nil {
					var page runtime.TranscriptSegmentPage
					var pageErr, retireErr error
					testsetup.RequireUntil(t, time.Now().Add(5*time.Second), time.Millisecond, func() bool {
						page, pageErr = engine.TranscriptNewestSegmentPage()
						if test.rollback == nil {
							return pageErr == nil && page.Snapshot.StreamingError == test.surface.Error()
						}
						retireErr = env.authority.WithRuntime(t.Context(), attachment.Resource(), func(context.Context, *runtime.Engine) error { return nil })
						return pageErr == nil && page.Snapshot.StreamingError == test.surface.Error() && errors.Is(retireErr, serverapi.ErrRuntimeUnavailable)
					}, "Runtime diagnostic/retirement = %q/%v/%v, want %q/unavailable", page.Snapshot.StreamingError, pageErr, retireErr, test.surface)
				}
			} else {
				requireTerminal(t, ack == nil && runErr != nil && runErr.Error() == test.diagnostic.Error(), "dormant result = %+v, %v", ack, runErr)
			}
			if test.outcome == nil {
				requireTerminal(t, len(env.publisher.outcomes) == 0, "Worktree outcomes = %+v, want none", env.publisher.outcomes)
			} else {
				outcomes := env.publisher.outcomes
				requireTerminal(t, len(outcomes) == 1 && outcomes[0].OperationID == operationID && outcomes[0].State == *test.outcome, "Worktree outcomes = %+v, want state=%v", outcomes, *test.outcome)
				if *test.outcome == failed {
					requireTerminal(t, outcomes[0].Failure != nil && (test.selector && outcomes[0].Failure.SelectorError != nil && outcomes[0].Failure.SelectorError.Kind == selectorFailure.Details.Kind && outcomes[0].Failure.SelectorError.Input == selectorFailure.Details.Input || !test.selector && outcomes[0].Failure.Diagnostic == test.diagnostic.Error()), "Worktree failure = %+v, want selector=%v diagnostic=%v", outcomes[0].Failure, selectorFailure.Details, test.diagnostic)
				}
			}
			failedOutcome := test.outcome != nil && *test.outcome == failed
			if test.publication != nil {
				target := mustResolveServiceTestTarget(t, env)
				requireTerminal(t, sessionTargetWorktreeID(target) == next.WorktreeID, "persisted target = %+v", target)
			}
			if failedOutcome && !test.selector {
				requireTerminal(t, len(restored) == 1, "technical restoration count = %d", len(restored))
				got := <-restored
				requireTerminal(t, got.ItemID.String() == operationID.String() && got.Kind == runtimeinput.PendingWorkItemKindWorktreeTransition &&
					got.CanonicalInput == "/wt switch "+next.WorktreeID, "technical restoration = %+v", got)
			} else {
				requireTerminal(t, len(restored) == 0, "unexpected technical restoration count = %d", len(restored))
			}
		})
	}
}
func runTerminalMutationCase(ctx context.Context, env *serviceTestEnv, nextWorktreeID string, previous clientui.SessionExecutionTarget, sync transitionTargetSync, writeFailure, syncFailure, rollbackFailure error, publicationFailure bool) error {
	_, err := applyWorktreeTargetMutation(
		func() error {
			if writeFailure != nil {
				return writeFailure
			}
			return env.store.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{SessionID: env.session.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: env.binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: nextWorktreeID}, CwdRelpath: "."})
		},
		func() (clientui.SessionExecutionTarget, error) {
			target, err := env.store.ResolveSessionExecutionTarget(ctx, env.session.Meta().SessionID)
			if err == nil {
				err = syncFailure
			}
			if err == nil && publicationFailure {
				err = sync(ctx, target, nil)
			}
			return target, worktreeUnappliedTechnicalUnlessClassified(err)
		},
		func() error {
			if rollbackFailure != nil {
				return rollbackFailure
			}
			return env.store.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdateFromReadModel(env.session.Meta().SessionID, previous))
		},
	)
	return err
}
func waitTerminal[T any](t *testing.T, ready <-chan T, name string) T {
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), time.Millisecond, func() bool { return len(ready) > 0 }, "timed out waiting for %s", name)
	return <-ready
}
func requireTerminal(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
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
