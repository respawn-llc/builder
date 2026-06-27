package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runprompt"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/runtimewire"
	"core/server/session"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcriptdiag"
)

const (
	ReasonRuntimeCanceled = "workflow_runtime_canceled"
	ReasonRuntimeFailed   = "workflow_runtime_failed"
)

var errWorkflowShellCompletionRequiresShell = errors.New("workflow shell_command completion requires shell tool availability for this run")

type RuntimeStore interface {
	GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error)
	GetRunStartContext(context.Context, workflow.RunID) (workflowstore.RunStartContext, error)
	AttachRunSession(context.Context, workflow.RunID, int64, string) error
	SetRunEffectiveCompletionMode(context.Context, workflow.RunID, int64, string) error
	SetRunWaitingAsk(context.Context, workflow.RunID, int64, string) error
	ClearRunWaitingAsk(context.Context, workflow.RunID, int64, string) error
	CompleteRun(context.Context, workflowstore.CompleteRunRequest) (workflowstore.CompleteRunResult, error)
	RecordProtocolViolation(context.Context, workflowstore.RecordProtocolViolationRequest) (workflowstore.RecordProtocolViolationResult, error)
	CountTaskComments(context.Context, workflow.TaskID) (int64, error)
	InterruptRun(context.Context, workflow.RunID, string, string) error
	InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error
}

type TaskWorktreeEnsurer interface {
	EnsureTaskWorktree(ctx context.Context, taskID string) error
}

type RuntimeEventRegistry interface {
	runtimewire.RuntimeRegistry
	PublishRuntimeEvent(sessionID string, evt runtime.Event)
	AwaitPromptResponse(ctx context.Context, sessionID string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error)
}

type Starter struct {
	cfg              config.App
	metadata         *metadata.Store
	store            RuntimeStore
	authManager      *auth.Manager
	background       *shelltool.Manager
	backgroundRouter runtimewire.BackgroundRouter
	runtimes         RuntimeEventRegistry
	storeOptions     []session.StoreOption
	clientFactory    func(SchedulerStartRunRequest) llm.Client
	worktrees        TaskWorktreeEnsurer
	finished         func(workflow.RunID, int64)

	mu     sync.Mutex
	cancel map[workflow.RunID]context.CancelFunc
	task   map[workflow.RunID]workflow.TaskID
	done   map[workflow.RunID]chan struct{}
	closed bool
	wg     sync.WaitGroup
}

type StarterOptions struct {
	ClientFactory func(SchedulerStartRunRequest) llm.Client
	Worktrees     TaskWorktreeEnsurer
}

func NewStarter(cfg config.App, metadataStore *metadata.Store, store RuntimeStore, authManager *auth.Manager, background *shelltool.Manager, backgroundRouter runtimewire.BackgroundRouter, runtimes RuntimeEventRegistry, opts StarterOptions) (*Starter, error) {
	if strings.TrimSpace(cfg.PersistenceRoot) == "" {
		return nil, errors.New("workflow runtime persistence root is required")
	}
	if metadataStore == nil {
		return nil, errors.New("workflow runtime metadata store is required")
	}
	if store == nil {
		return nil, errors.New("workflow runtime store is required")
	}
	return &Starter{
		cfg:              cfg,
		metadata:         metadataStore,
		store:            store,
		authManager:      authManager,
		background:       background,
		backgroundRouter: backgroundRouter,
		runtimes:         runtimes,
		storeOptions:     metadataStore.AuthoritativeSessionStoreOptions(),
		clientFactory:    opts.ClientFactory,
		worktrees:        opts.Worktrees,
		cancel:           map[workflow.RunID]context.CancelFunc{},
		task:             map[workflow.RunID]workflow.TaskID{},
		done:             map[workflow.RunID]chan struct{}{},
	}, nil
}

func (s *Starter) SetRuntimeFinished(fn func(workflow.RunID, int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = fn
}

func (s *Starter) StartWorkflowRun(ctx context.Context, req SchedulerStartRunRequest) error {
	if strings.TrimSpace(string(req.RunID)) == "" {
		return errors.New("workflow run id is required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("workflow runtime starter closed")
	}
	s.mu.Unlock()
	if s.worktrees != nil {
		if err := s.worktrees.EnsureTaskWorktree(ctx, string(req.TaskID)); err != nil {
			return err
		}
	}
	input, err := s.store.GetRunStartContext(ctx, req.RunID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.WorktreeID) == "" || strings.TrimSpace(input.WorktreeRoot) == "" {
		return fmt.Errorf("workflow task %q has no managed worktree", input.Task.ID)
	}
	if input.Run.Generation != req.Generation {
		return fmt.Errorf("stale workflow run generation: got %d want %d", req.Generation, input.Run.Generation)
	}
	if err := s.validateRole(input.Node.SubagentRole); err != nil {
		return err
	}
	plan, warnings, err := s.planSession(ctx, input)
	if err != nil {
		return err
	}
	// When the plan reuses an existing session (resume, continue, or in-place
	// compact-and-continue), it is the previous node's persisted session — never
	// dispose of it on setup failure. Only freshly created run sessions
	// (new-session and fan-out clones) are disposable.
	//
	// For reused sessions, snapshot previous listing/reminder metadata so setup
	// mutations can be rolled back if any later setup step fails.
	var prevReminderState *session.WorktreeReminderState
	var prevListingMetadata *sessionListingMetadata
	if reusesExistingSession(input) {
		meta := plan.Store.Meta()
		prevListingMetadata = &sessionListingMetadata{Name: meta.Name, FirstPromptPreview: meta.FirstPromptPreview}
		if wr := meta.WorktreeReminder; wr != nil {
			snap := *wr
			prevReminderState = &snap
		}
	}
	cleanupSession := func() error {
		if reusesExistingSession(input) {
			return errors.Join(restoreSessionListingMetadata(plan.Store, prevListingMetadata), plan.Store.SetWorktreeReminderState(prevReminderState))
		}
		return s.cleanupSession(ctx, plan.Store)
	}
	if err := applyWorkflowSessionMetadata(input, &plan); err != nil {
		return errors.Join(err, cleanupSession())
	}
	client := llm.Client(nil)
	if s.clientFactory != nil {
		client = s.clientFactory(req)
	}
	effectiveMode, client, err := s.resolveAndPersistWorkflowCompletionMode(ctx, req, input, plan, client)
	if err != nil {
		return errors.Join(err, cleanupSession())
	}
	if err := plan.Store.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		WorktreePath:  input.WorktreeRoot,
		WorkspaceRoot: input.WorkspaceRoot,
		EffectiveCwd:  input.WorktreeRoot,
	}); err != nil {
		return errors.Join(err, cleanupSession())
	}
	runCtx, cancel := context.WithCancel(context.Background())
	if !s.registerRun(req, cancel) {
		cancel()
		return errors.Join(errors.New("workflow runtime starter closed"), cleanupSession())
	}
	if err := s.metadata.UpdateSessionExecutionTargetByID(ctx, plan.Store.Meta().SessionID, input.WorkspaceID, input.WorktreeID, "."); err != nil {
		cancel()
		s.releaseRegisteredRun(req.RunID)
		return errors.Join(err, cleanupSession())
	}
	var previousWorkflowSession *session.WorkflowSessionState
	if workflowSession := plan.Store.Meta().WorkflowSession; workflowSession != nil {
		snap := *workflowSession
		previousWorkflowSession = &snap
	}
	restoreWorkflowSession := func() error {
		return plan.Store.SetWorkflowSessionState(previousWorkflowSession)
	}
	if err := plan.Store.SetWorkflowSessionState(&session.WorkflowSessionState{
		RunID:      string(req.RunID),
		TaskID:     string(input.Task.ID),
		WorkflowID: string(input.Task.WorkflowID),
	}); err != nil {
		cancel()
		s.releaseRegisteredRun(req.RunID)
		return errors.Join(err, cleanupSession())
	}
	if err := s.store.AttachRunSession(ctx, req.RunID, req.Generation, plan.Store.Meta().SessionID); err != nil {
		cancel()
		s.releaseRegisteredRun(req.RunID)
		return errors.Join(err, restoreWorkflowSession(), cleanupSession())
	}
	go s.run(runCtx, req, input, plan, warnings, client, effectiveMode)
	return nil
}

func (s *Starter) registerRun(req SchedulerStartRunRequest, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.cancel[req.RunID] = cancel
	s.task[req.RunID] = req.TaskID
	s.done[req.RunID] = make(chan struct{})
	s.wg.Add(1)
	return true
}

func (s *Starter) releaseRegisteredRun(runID workflow.RunID) {
	s.mu.Lock()
	done := s.done[runID]
	delete(s.cancel, runID)
	delete(s.task, runID)
	delete(s.done, runID)
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
	s.wg.Done()
}

func (s *Starter) cleanupSession(ctx context.Context, store *session.Store) error {
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx := context.WithoutCancel(ctx)
	sessionID := store.Meta().SessionID
	return errors.Join(store.RemoveDurable(), s.metadata.DeleteSessionRecordByID(cleanupCtx, sessionID))
}

func (s *Starter) Close() error {
	s.mu.Lock()
	s.closed = true
	cancels := make([]context.CancelFunc, 0, len(s.cancel))
	for _, cancel := range s.cancel {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	s.wg.Wait()
	return nil
}

func (s *Starter) CancelTaskRuns(ctx context.Context, taskID workflow.TaskID) error {
	s.mu.Lock()
	cancels := []context.CancelFunc{}
	done := []<-chan struct{}{}
	for runID, cancel := range s.cancel {
		if s.task[runID] == taskID && cancel != nil {
			cancels = append(cancels, cancel)
			if runDone := s.done[runID]; runDone != nil {
				done = append(done, runDone)
			}
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, runDone := range done {
		select {
		case <-runDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Starter) CancelRun(ctx context.Context, runID workflow.RunID) error {
	s.mu.Lock()
	cancel := s.cancel[runID]
	runDone := s.done[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if runDone == nil {
		return nil
	}
	select {
	case <-runDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (s *Starter) RequestCancelRun(runID workflow.RunID) bool {
	s.mu.Lock()
	cancel := s.cancel[runID]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// reusesExistingSession reports whether planSession reuses a pre-existing
// session (resume of a started run, continue_session, or in-place
// compact_and_continue_session) rather than creating a disposable one
// (new_session or a fan-out clone). Reused sessions belong to a prior node and
// must not be cleaned up on setup failure.
func reusesExistingSession(input workflowstore.RunStartContext) bool {
	if strings.TrimSpace(input.Run.SessionID) != "" {
		return true
	}
	switch input.ContextMode {
	case workflow.ContextModeContinueSession:
		return true
	case workflow.ContextModeCompactAndContinueSession:
		return !input.IsFanoutBranch
	default:
		return false
	}
}

func (s *Starter) planSession(ctx context.Context, input workflowstore.RunStartContext) (launch.SessionPlan, []string, error) {
	cfg := s.cfg
	cfg.WorkspaceRoot = strings.TrimSpace(input.WorkspaceRoot)
	projectID := strings.TrimSpace(input.Task.ProjectID)
	if projectID == "" {
		return launch.SessionPlan{}, nil, errors.New("workflow task project id is required")
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), projectID, "sessions")
	planner := launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: s.storeOptions,
		MetadataStoreOpener: func(string) (launch.MetadataExecutionTargetStore, error) {
			return s.metadata, nil
		},
	}
	// A fan-out branch creates a brand-new disposable clone before the rest of
	// planning runs. If any later planning step fails, StartWorkflowRun's cleanup
	// hook never sees it, so remove the clone here on failure to avoid orphaning
	// an unattached session directory.
	disposableCloneID := ""
	planSucceeded := false
	defer func() {
		if !planSucceeded && disposableCloneID != "" {
			s.removeFanoutClone(ctx, containerDir, disposableCloneID)
		}
	}()
	var plan launch.SessionPlan
	var err error
	overrides := workflowRunPromptOverrides(input.Node.SubagentRole)
	skipPersistedRoleValidation := overrides.HasAny()
	if strings.TrimSpace(input.Run.SessionID) != "" {
		plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, SelectedSessionID: input.Run.SessionID, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		if err != nil {
			return launch.SessionPlan{}, nil, err
		}
		if err := plan.Store.EnsureDurable(); err != nil {
			return launch.SessionPlan{}, nil, err
		}
	} else {
		switch input.ContextMode {
		case "", workflow.ContextModeNewSession:
			plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, ForceNewSession: true, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		case workflow.ContextModeContinueSession:
			if strings.TrimSpace(input.SourceSessionID) == "" {
				return launch.SessionPlan{}, nil, errors.New("continue_session requires a source session")
			}
			plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, SelectedSessionID: input.SourceSessionID, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		case workflow.ContextModeCompactAndContinueSession:
			if strings.TrimSpace(input.SourceSessionID) == "" {
				return launch.SessionPlan{}, nil, errors.New("compact_and_continue_session requires a source session")
			}
			// In-place continuation reuses the source session; the runtime runs a real
			// compaction before the node turn. A fan-out branch instead continues in an
			// isolated full clone of the source so parallel branches never compact or
			// mutate the shared source session concurrently.
			continuationSessionID := input.SourceSessionID
			if input.IsFanoutBranch {
				continuationSessionID, err = s.cloneSourceSessionForFanout(containerDir, input.SourceSessionID)
				if err != nil {
					return launch.SessionPlan{}, nil, err
				}
				disposableCloneID = continuationSessionID
			}
			plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, SelectedSessionID: continuationSessionID, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		default:
			return launch.SessionPlan{}, nil, fmt.Errorf("unsupported workflow context mode %q", input.ContextMode)
		}
		if err != nil {
			return launch.SessionPlan{}, nil, err
		}
		if err := plan.Store.EnsureDurable(); err != nil {
			return launch.SessionPlan{}, nil, err
		}
	}
	warnings := []string{}
	plan, warnings, err = launch.ApplyRunPromptOverrides(plan, overrides, auth.EmptyState())
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	planSucceeded = true
	return plan, warnings, nil
}

type sessionListingMetadata struct {
	Name               string
	FirstPromptPreview string
}

func restoreSessionListingMetadata(store *session.Store, metadata *sessionListingMetadata) error {
	if store == nil || metadata == nil {
		return nil
	}
	return store.SetListingMetadata(metadata.Name, metadata.FirstPromptPreview)
}

func applyWorkflowSessionMetadata(input workflowstore.RunStartContext, plan *launch.SessionPlan) error {
	if plan == nil || plan.Store == nil {
		return errors.New("workflow session plan store is required")
	}
	name, err := workflowSessionName(input)
	if err != nil {
		return err
	}
	preview, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		return err
	}
	if err := plan.Store.SetListingMetadata(name, preview); err != nil {
		return err
	}
	plan.SessionName = plan.Store.Meta().Name
	return nil
}

func workflowSessionName(input workflowstore.RunStartContext) (string, error) {
	taskDisplayID := strings.TrimSpace(input.Task.ShortID)
	if taskDisplayID == "" {
		taskDisplayID = strings.TrimSpace(string(input.Task.ID))
	}
	if taskDisplayID == "" {
		return "", errors.New("workflow session metadata requires a task id")
	}
	sourceDisplayName := strings.TrimSpace(input.AcceptedTransitionPath.SourceNodeDisplayName)
	if sourceDisplayName == "" {
		return "", errors.New("workflow session metadata requires accepted transition source display name")
	}
	targetDisplayName := strings.TrimSpace(input.AcceptedTransitionPath.TargetNodeDisplayName)
	if targetDisplayName == "" {
		return "", errors.New("workflow session metadata requires accepted transition target display name")
	}
	return fmt.Sprintf("%s: %s -> %s", taskDisplayID, sourceDisplayName, targetDisplayName), nil
}

func (s *Starter) resolveAndPersistWorkflowCompletionMode(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext, plan launch.SessionPlan, client llm.Client) (workflowruntime.CompletionMode, llm.Client, error) {
	shellAvailable := toolIDEnabled(plan.EnabledTools, toolspec.ToolExecCommand)
	if stored := strings.TrimSpace(input.Run.EffectiveCompletionMode); stored != "" {
		mode, err := workflowruntime.ParseCompletionMode(stored)
		if err != nil {
			return "", client, err
		}
		if mode == workflowruntime.CompletionModeShellCommand && !shellAvailable {
			return "", client, errWorkflowShellCompletionRequiresShell
		}
		return mode, client, nil
	}
	configuredMode := s.cfg.Settings.Workflow.CompletionMode
	if nodeMode := strings.TrimSpace(input.Node.CompletionMode); nodeMode != "" {
		configuredMode = config.WorkflowCompletionMode(nodeMode)
	}
	if configuredMode == config.WorkflowCompletionModeShellCommand && !shellAvailable {
		return "", client, errWorkflowShellCompletionRequiresShell
	}
	selection := workflowruntime.CompletionModeSelection{
		ConfiguredMode:         configuredMode,
		HasContinueSessionEdge: input.WorkflowHasContinueSessionEdge,
		ShellAvailable:         shellAvailable,
	}
	resolvedClient := client
	if workflowCompletionModeNeedsProviderCapabilities(selection) {
		caps, nextClient, err := s.workflowProviderCapabilities(ctx, plan, client)
		if err != nil {
			return "", nextClient, fmt.Errorf("resolve provider capabilities for workflow completion: %w", err)
		}
		selection.ProviderCapabilities = caps
		resolvedClient = nextClient
	}
	mode, err := workflowruntime.SelectCompletionMode(selection)
	if err != nil {
		return "", resolvedClient, err
	}
	if err := s.store.SetRunEffectiveCompletionMode(ctx, req.RunID, req.Generation, string(mode)); err != nil {
		return "", resolvedClient, err
	}
	return mode, resolvedClient, nil
}

func workflowCompletionModeNeedsProviderCapabilities(selection workflowruntime.CompletionModeSelection) bool {
	switch selection.ConfiguredMode {
	case config.WorkflowCompletionModeStructuredOutput:
		return true
	case config.WorkflowCompletionModeAuto, "":
		return selection.ShellAvailable && !selection.HasContinueSessionEdge
	default:
		return false
	}
}

func (s *Starter) workflowProviderCapabilities(ctx context.Context, plan launch.SessionPlan, client llm.Client) (llm.ProviderCapabilities, llm.Client, error) {
	if caps, ok := llm.ProviderCapabilitiesFromLocked(plan.Store.Meta().Locked); ok {
		return caps, client, nil
	}
	if caps, ok := llm.ProviderCapabilitiesFromOverride(plan.ActiveSettings.ProviderCapabilities); ok {
		return caps, client, nil
	}
	if client == nil {
		created, err := s.newWorkflowProviderClient(plan.ActiveSettings)
		if err != nil {
			return llm.ProviderCapabilities{}, nil, err
		}
		client = created
	}
	provider, ok := client.(llm.ProviderCapabilitiesClient)
	if !ok {
		return llm.ProviderCapabilities{}, client, fmt.Errorf("provider capabilities are unavailable for client %T", client)
	}
	caps, err := provider.ProviderCapabilities(ctx)
	if err != nil {
		return llm.ProviderCapabilities{}, client, err
	}
	return caps, client, nil
}

func (s *Starter) newWorkflowProviderClient(active config.Settings) (llm.Client, error) {
	var authProvider llm.AuthHeaderProvider
	if s.authManager != nil {
		authProvider = s.authManager
	}
	override, _ := llm.ProviderCapabilitiesFromOverride(active.ProviderCapabilities)
	var overridePtr *llm.ProviderCapabilities
	if strings.TrimSpace(override.ProviderID) != "" {
		overridePtr = &override
	}
	return llm.NewProviderClient(llm.ProviderClientOptions{
		Provider:                     llm.Provider(strings.TrimSpace(active.ProviderOverride)),
		Model:                        active.Model,
		Auth:                         authProvider,
		HTTPClient:                   llm.NewHTTPClient(time.Duration(active.Timeouts.ModelRequestSeconds) * time.Second),
		OpenAIBaseURL:                active.OpenAIBaseURL,
		ModelVerbosity:               string(active.ModelVerbosity),
		Store:                        active.Store,
		ContextWindowTokens:          active.ModelContextWindow,
		ProviderCapabilitiesOverride: overridePtr,
	})
}

func toolIDEnabled(enabled []toolspec.ID, want toolspec.ID) bool {
	for _, id := range enabled {
		if id == want {
			return true
		}
	}
	return false
}

func workflowRunPromptOverrides(role string) serverapi.RunPromptOverrides {
	if workflow.IsDefaultAgentRole(role) {
		return serverapi.RunPromptOverrides{AgentRole: workflow.DefaultAgentRole}
	}
	return serverapi.RunPromptOverrides{AgentRole: role}
}

// cloneSourceSessionForFanout creates an isolated full clone of the source
// session for a fan-out compact-and-continue branch and returns its session ID,
// so the branch can be compacted/continued without touching the shared source.
func (s *Starter) cloneSourceSessionForFanout(containerDir, sourceSessionID string) (string, error) {
	sourceDir, err := session.ResolveScopedSessionDir(containerDir, sourceSessionID)
	if err != nil {
		return "", fmt.Errorf("resolve source session dir: %w", err)
	}
	sourceStore, err := session.Open(sourceDir, s.storeOptions...)
	if err != nil {
		return "", fmt.Errorf("open source session: %w", err)
	}
	cloned, err := session.CloneSession(sourceStore, "")
	if err != nil {
		return "", fmt.Errorf("clone source session: %w", err)
	}
	return cloned.Meta().SessionID, nil
}

// removeFanoutClone deletes a disposable fan-out clone that was created but never
// attached to a started run because planning failed afterward. Best-effort: it
// removes the on-disk session and any metadata record, leaving nothing orphaned.
func (s *Starter) removeFanoutClone(ctx context.Context, containerDir, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if dir, err := session.ResolveScopedSessionDir(containerDir, sessionID); err == nil {
		if store, err := session.Open(dir, s.storeOptions...); err == nil {
			_ = store.RemoveDurable()
		}
	}
	_ = s.metadata.DeleteSessionRecordByID(cleanupCtx, sessionID)
}

func (s *Starter) validateRole(role string) error {
	trimmed := strings.TrimSpace(role)
	if workflow.IsDefaultAgentRole(trimmed) {
		return nil
	}
	for _, available := range config.AvailableSubagentRoleNames(s.cfg.Settings, false) {
		if available == trimmed {
			return nil
		}
	}
	return fmt.Errorf("workflow validation failed: [%s]", workflow.CodeAgentRoleMissing)
}

func (s *Starter) run(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext, plan launch.SessionPlan, warnings []string, client llm.Client, effectiveMode workflowruntime.CompletionMode) {
	defer s.wg.Done()
	defer s.finish(req.RunID, req.Generation)
	logger, err := runprompt.NewRunLogger(plan.Store.Dir(), nil)
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonRuntimeFailed, err)
		return
	}
	defer func() { _ = logger.Close() }()
	logger.Logf("workflow.runtime.start run_id=%s task_id=%s session_id=%s node_id=%s worktree=%s model=%s", req.RunID, req.TaskID, plan.Store.Meta().SessionID, req.NodeID, input.WorktreeRoot, plan.ActiveSettings.Model)
	for _, warning := range warnings {
		logger.Logf("workflow.runtime.warning %s", warning)
	}
	instructions, instructionsErr := BuildWorkflowTaskInstructions(input)
	if instructionsErr != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonRuntimeFailed, instructionsErr)
		return
	}
	wiring, err := runtimewire.NewRuntimeWiringWithBackground(plan.Store, plan.ActiveSettings, workflowRuntimeEnabledTools(plan.EnabledTools), input.WorktreeRoot, s.authManager, logger, s.background, runtimewire.RuntimeWiringOptions{
		Headless:        true,
		FastMode:        nil,
		Sources:         plan.Source.Sources,
		Client:          client,
		GlobalConfigDir: s.cfg.PersistenceRoot,
		WorkflowRun: &workflowruntime.Config{
			RunID:                        req.RunID,
			Contract:                     workflowCompletionContract(req, input),
			CompletionMode:               effectiveMode,
			MaxInvalidCompletionAttempts: s.cfg.Settings.Workflow.MaxInvalidCompletionAttempts,
			Controller:                   workflowruntime.StoreController{Store: s.store},
			TaskCommentCounter:           s.store,
			Instructions:                 instructions,
		},
		OnEvent: func(evt runtime.Event) {
			logger.Logf("%s", runprompt.FormatRuntimeEvent(evt))
			if transcriptdiag.Enabled(plan.ActiveSettings.Debug, os.Getenv) {
				projected := runtimeview.EventFromRuntime(evt)
				logger.Logf("%s", runprompt.FormatTranscriptProjectionDiagnostic(plan.Store.Meta().SessionID, projected))
				logger.Logf("%s", runprompt.FormatTranscriptPublishDiagnostic(plan.Store.Meta().SessionID, projected))
			}
			if s.runtimes != nil {
				s.runtimes.PublishRuntimeEvent(plan.Store.Meta().SessionID, evt)
			}
		},
	})
	if err != nil {
		s.interrupt(context.Background(), req.RunID, req.Generation, ReasonRuntimeFailed, err)
		return
	}
	defer func() { _ = wiring.Close() }()
	var runtimeRegistry runtimewire.RuntimeRegistry
	if s.runtimes != nil {
		runtimeRegistry = s.runtimes
	}
	if wiring.AskBroker != nil && s.runtimes != nil {
		sessionID := plan.Store.Meta().SessionID
		wiring.AskBroker.SetAskHandler(func(askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
			if err := s.store.SetRunWaitingAsk(context.Background(), req.RunID, req.Generation, askReq.ID); err != nil {
				return askquestion.AskQuestionResponse{}, err
			}
			resp, askErr := s.runtimes.AwaitPromptResponse(ctx, sessionID, askReq)
			if clearErr := s.store.ClearRunWaitingAsk(context.Background(), req.RunID, req.Generation, askReq.ID); clearErr != nil && askErr == nil {
				return askquestion.AskQuestionResponse{}, clearErr
			}
			return resp, askErr
		})
	} else if wiring.AskBroker != nil {
		wiring.AskBroker.SetAskHandler(func(askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
			return askquestion.AskQuestionResponse{}, errors.New("workflow questions require runtime registry")
		})
	}
	var rebind func(string) error
	if wiring.LocalTools != nil {
		rebind = runtimewire.RuntimeRebindFunc(wiring.LocalTools.Rebind, wiring.Engine)
	}
	sessionID := plan.Store.Meta().SessionID
	failQueuedOnClose := false
	registration := runtimewire.RegisterSessionRuntime(sessionID, wiring.Engine, runtimeRegistry, s.backgroundRouter, runtimewire.WithRuntimeRebind(rebind))
	defer func() {
		_ = registration.CloseWithDrain(context.Background(), func(drainCtx context.Context) error {
			if failQueuedOnClose {
				wiring.Engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureClosing)
				return nil
			}
			return wiring.Engine.DrainQueuedUserMessagesBeforeClose(drainCtx)
		})
	}()
	// Compact exactly once per compact_and_continue handoff. The compaction's
	// provenance is recorded atomically in its history_replaced event and rebuilt
	// on restore, so the engine reports which run last compacted this session.
	// Gating on a populated input.Run.SessionID is wrong because AttachRunSession
	// persists the reused source session before CompactContext commits, so a run
	// interrupted mid compaction would skip it on resume and continue from
	// un-compacted history. Keying on the recorded run ID instead: a resumed run
	// (same ID) whose compaction committed skips; one interrupted before commit
	// recompacts; a later in-place handoff (new run ID, same session) compacts
	// again because its continuation compaction is always the run's first action.
	if input.ContextMode == workflow.ContextModeCompactAndContinueSession &&
		wiring.Engine.LastCompactionWorkflowRunID() != string(req.RunID) {
		if err := wiring.Engine.CompactContext(ctx, ""); err != nil {
			failQueuedOnClose = true
			reason := ReasonRuntimeFailed
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				reason = ReasonRuntimeCanceled
			}
			s.interrupt(context.Background(), req.RunID, req.Generation, reason, err)
			return
		}
	}
	_, err = wiring.Engine.SubmitWorkflowTurn(ctx)
	if err != nil {
		failQueuedOnClose = true
		reason := ReasonRuntimeFailed
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			reason = ReasonRuntimeCanceled
		}
		s.interrupt(context.Background(), req.RunID, req.Generation, reason, err)
	}
}

func (s *Starter) finish(runID workflow.RunID, generation int64) {
	s.mu.Lock()
	done := s.done[runID]
	delete(s.cancel, runID)
	delete(s.task, runID)
	delete(s.done, runID)
	finished := s.finished
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
	if finished != nil {
		finished(runID, generation)
	}
}

func workflowRuntimeEnabledTools(enabled []toolspec.ID) []toolspec.ID {
	out := make([]toolspec.ID, 0, len(enabled))
	for _, id := range enabled {
		out = append(out, id)
	}
	return out
}

func (s *Starter) interrupt(ctx context.Context, runID workflow.RunID, generation int64, reason string, cause error) {
	detail := "{}"
	if cause != nil {
		if raw, err := json.Marshal(map[string]string{"error": cause.Error()}); err == nil {
			detail = string(raw)
		}
	}
	if err := s.store.InterruptRunGeneration(ctx, runID, generation, reason, detail); err != nil {
		return
	}
}

func BuildWorkflowTaskInstructions(input workflowstore.RunStartContext) (workflowruntime.TaskInstructions, error) {
	nodePrompt, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		return workflowruntime.TaskInstructions{}, err
	}
	taskShortID := strings.TrimSpace(input.Task.ShortID)
	if taskShortID == "" {
		taskShortID = string(input.Task.ID)
	}
	workflowShortID := strings.TrimSpace(string(input.Workflow.ID))
	if workflowShortID == "" {
		workflowShortID = string(input.Task.WorkflowID)
	}
	return workflowruntime.TaskInstructions{
		TaskID:          string(input.Task.ID),
		TaskShortID:     taskShortID,
		TaskTitle:       strings.TrimSpace(input.Task.Title),
		TaskBody:        strings.TrimSpace(input.Task.Body),
		WorkflowID:      string(input.Task.WorkflowID),
		WorkflowShortID: workflowShortID,
		NodeID:          string(input.Node.ID),
		NodeKey:         string(input.Node.Key),
		NodeDisplayName: strings.TrimSpace(input.Node.DisplayName),
		ContextMode:     string(input.ContextMode),
		SourceSessionID: strings.TrimSpace(input.SourceSessionID),
		Transitions:     workflowInstructionTransitions(input.TransitionOptions, input.TransitionIDs),
		NodePrompt:      nodePrompt,
	}, nil
}

func workflowTransitions(options []workflowstore.TransitionOption, transitionIDs []string) []prompts.WorkflowTransition {
	capacity := len(options)
	if len(transitionIDs) > capacity {
		capacity = len(transitionIDs)
	}
	out := make([]prompts.WorkflowTransition, 0, capacity)
	if len(options) > 0 {
		for _, option := range options {
			id := strings.TrimSpace(option.ID)
			if id == "" {
				continue
			}
			out = append(out, prompts.WorkflowTransition{ID: id, DisplayName: strings.TrimSpace(option.DisplayName), Description: strings.TrimSpace(option.Description)})
		}
		return out
	}
	for _, id := range transitionIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, prompts.WorkflowTransition{ID: trimmed})
		}
	}
	return out
}

func workflowInstructionTransitions(options []workflowstore.TransitionOption, transitionIDs []string) []workflowruntime.TransitionInstruction {
	transitions := workflowTransitions(options, transitionIDs)
	out := make([]workflowruntime.TransitionInstruction, 0, len(transitions))
	for _, transition := range transitions {
		out = append(out, workflowruntime.TransitionInstruction{ID: transition.ID, DisplayName: transition.DisplayName, Description: transition.Description})
	}
	return out
}

func workflowCompletionContract(req SchedulerStartRunRequest, input workflowstore.RunStartContext) workflowruntime.CompletionContract {
	return workflowruntime.CompletionContract{
		RunID:              req.RunID,
		ExpectedGeneration: req.Generation,
		RequireGeneration:  true,
		Transitions:        workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs),
	}
}

func workflowCompletionTransitions(options []workflowstore.TransitionOption, transitionIDs []string) []workflowruntime.CompletionTransition {
	out := make([]workflowruntime.CompletionTransition, 0, len(options))
	if len(options) > 0 {
		for _, option := range options {
			id := strings.TrimSpace(option.ID)
			if id == "" {
				continue
			}
			out = append(out, workflowruntime.CompletionTransition{
				ID:          id,
				DisplayName: strings.TrimSpace(option.DisplayName),
				Description: strings.TrimSpace(option.Description),
				Parameters:  append([]workflow.Parameter(nil), option.Parameters...),
			})
		}
		return out
	}
	for _, id := range transitionIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, workflowruntime.CompletionTransition{ID: trimmed})
		}
	}
	return out
}

type nodePromptTemplateData struct {
	TaskId          string
	TaskShortId     string
	TaskTitle       string
	TaskBody        string
	NodeId          string
	NodeKey         string
	NodeDisplayName string
	Params          map[string]promptParameterNamespace
}

const currentParameterValueKey = "\x00current"

type promptParameterNamespace map[string]string

func (n promptParameterNamespace) String() string {
	return n[currentParameterValueKey]
}

func renderTransitionPrompt(templateText string, input workflowstore.RunStartContext) (string, error) {
	prompt := strings.TrimSpace(templateText)
	if prompt == "" {
		return "", nil
	}
	tmpl, err := template.New("workflow transition prompt").Option("missingkey=error").Parse(prompt)
	if err != nil {
		return "", fmt.Errorf("parse workflow transition prompt template: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, nodePromptTemplateData{
		TaskId:          string(input.Task.ID),
		TaskShortId:     strings.TrimSpace(input.Task.ShortID),
		TaskTitle:       strings.TrimSpace(input.Task.Title),
		TaskBody:        strings.TrimSpace(input.Task.Body),
		NodeId:          string(input.Node.ID),
		NodeKey:         string(input.Node.Key),
		NodeDisplayName: strings.TrimSpace(input.Node.DisplayName),
		Params:          promptParameterData(input.ParameterValues, input.PriorParameterValues),
	}); err != nil {
		return "", fmt.Errorf("render workflow transition prompt template: %w", err)
	}
	return b.String(), nil
}

func promptParameterData(current map[string]string, prior map[string]map[string]string) map[string]promptParameterNamespace {
	out := map[string]promptParameterNamespace{}
	for transitionKey, values := range prior {
		key := strings.TrimSpace(transitionKey)
		if key == "" {
			continue
		}
		namespace := out[key]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		for parameterKey, value := range values {
			trimmedParameterKey := strings.TrimSpace(parameterKey)
			if trimmedParameterKey != "" {
				namespace[trimmedParameterKey] = value
			}
		}
		out[key] = namespace
	}
	for parameterKey, value := range current {
		key := strings.TrimSpace(parameterKey)
		if key == "" {
			continue
		}
		namespace := out[key]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		namespace[currentParameterValueKey] = value
		out[key] = namespace
	}
	return out
}

var _ SchedulerRuntimeStarter = (*Starter)(nil)
