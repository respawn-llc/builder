package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

const (
	ReasonRuntimeCanceled = "workflow_runtime_canceled"
	ReasonRuntimeFailed   = "workflow_runtime_failed"
)

type RuntimeStore interface {
	ResolveCurrentNodeStartContext(context.Context, workflow.CurrentNodeReference) (workflowstore.CurrentNodeStartContext, error)
	BindSessionToCurrentNode(context.Context, workflowstore.TaskSessionAssociationRequest) (workflowstore.TaskSessionAssociation, error)
	CountTaskComments(context.Context, workflow.TaskID) (int64, error)
}

type WorkflowAttentionRegistry interface {
	workflowattention.QuestionAttentionRegistry
	workflowattention.ApprovalQuestionAttentionRegistry
}

type Starter struct {
	cfg                  config.App
	metadata             *metadata.Store
	store                RuntimeStore
	authManager          *auth.Manager
	attention            WorkflowAttentionRegistry
	runtimeAuthority     *sessionruntime.Authority
	storeOptions         []session.StoreOption
	runtimeClientFactory runtimewire.RuntimeClientFactory
	mutationPermit       *workflowexecution.MutationPermit
	closed               atomic.Bool
}

type StarterOptions struct {
	RuntimeClientFactory runtimewire.RuntimeClientFactory
	RuntimeAuthority     *sessionruntime.Authority
	MutationPermit       *workflowexecution.MutationPermit
}

func NewStarter(cfg config.App, metadataStore *metadata.Store, store RuntimeStore, authManager *auth.Manager, attention WorkflowAttentionRegistry, opts StarterOptions) (*Starter, error) {
	if strings.TrimSpace(cfg.PersistenceRoot) == "" {
		return nil, errors.New("workflow runtime persistence root is required")
	}
	if metadataStore == nil || store == nil || opts.RuntimeAuthority == nil || opts.MutationPermit == nil {
		return nil, errors.New("workflow runtime dependencies are required")
	}
	return &Starter{
		cfg:                  cfg,
		metadata:             metadataStore,
		store:                store,
		authManager:          authManager,
		attention:            attention,
		runtimeAuthority:     opts.RuntimeAuthority,
		storeOptions:         metadataStore.AuthoritativeSessionStoreOptions(),
		runtimeClientFactory: opts.RuntimeClientFactory,
		mutationPermit:       opts.MutationPermit,
	}, nil
}

func (s *Starter) StartCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	if s.closed.Load() {
		return errors.New("workflow runtime starter closed")
	}
	if !lease.Workflow().CurrentNode.Equal(reference) {
		return errors.New("workflow execution lease does not match current node")
	}
	input, err := s.store.ResolveCurrentNodeStartContext(ctx, reference)
	if err != nil {
		return err
	}
	switch input.Node.Kind {
	case workflow.NodeKindScript:
		return s.startCurrentNodeScript(ctx, input, lease, controller)
	case workflow.NodeKindAgent:
		if err := s.validateRole(input.Node.SubagentRole); err != nil {
			return err
		}
		return s.startCurrentNodeAgent(ctx, input, lease, controller)
	default:
		return fmt.Errorf("current node %v is not executable", reference)
	}
}

func (s *Starter) startCurrentNodeAgent(ctx context.Context, input workflowstore.CurrentNodeStartContext, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	root, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return err
	}
	plan, disposable, err := s.planCurrentNodeSession(ctx, input, root)
	if err != nil {
		return err
	}
	cleanup := func(err error) error {
		if !disposable {
			return err
		}
		return errors.Join(err, s.cleanupSession(context.WithoutCancel(ctx), plan.Descriptor))
	}
	if err := s.applyCurrentNodeSessionMetadata(ctx, input, &plan); err != nil {
		return cleanup(err)
	}
	client, err := s.newWorkflowProviderClient(ctx, plan)
	if err != nil {
		return cleanup(err)
	}
	mode, client, err := s.resolveCurrentNodeCompletionMode(ctx, input, plan, client)
	if err != nil {
		return cleanup(err)
	}
	if _, err := s.store.BindSessionToCurrentNode(ctx, workflowstore.TaskSessionAssociationRequest{
		SessionID: plan.Descriptor.SessionID(), CurrentNode: input.CurrentNode.Reference, AssociatedAt: time.Now().UTC(),
	}); err != nil {
		return cleanup(err)
	}
	if err := s.applyCurrentNodeSessionExecutionTarget(ctx, input, plan.Descriptor); err != nil {
		return cleanup(err)
	}
	runtimeConfig, err := BuildCurrentNodeRuntimeConfig(input, lease, mode, s.cfg.Settings.Workflow.MaxInvalidCompletionAttempts, plan.ActiveSettings.Workflow.UseRequiredToolCalls, controller, s.store)
	if err != nil {
		return cleanup(err)
	}
	pathContext, err := currentNodeManagedWorktreePathContext(plan, root)
	if err != nil {
		return cleanup(err)
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: plan.ActiveSettings, EnabledTools: workflowRuntimeEnabledTools(plan.EnabledTools), Workdir: root.EffectiveRoot(),
		ManagedWorktreePathContext: pathContext, Sources: plan.Source.Sources, Headless: true, Client: client,
		ReviewerClientFactory: s.runtimeClientFactory, WorkflowRun: runtimeConfig,
		SkipContinuationAgentRoleValidation: workflowPromptOverrides(input.Node.SubagentRole).HasAny(),
		StartLogLines:                       []string{fmt.Sprintf("workflow.runtime.start task_id=%s session_id=%s node_id=%s execution_root=%s model=%s", input.Task.ID, plan.Descriptor.SessionID(), input.Node.ID, root.EffectiveRoot(), plan.ActiveSettings.Model)},
		AskQuestionBatchSkipped: func(batch askquestion.AskQuestionBatchMetadata) {
			if s.attention == nil {
				return
			}
			if err := workflowattention.PrepareSkippedTaskQuestionBatch(s.attention, currentNodeQuestionContext(input, plan.Descriptor.SessionID().String()), batch, time.Now().UTC()); err != nil {
				slog.Warn("prepare skipped current-node workflow question batch failed", "task_id", input.Task.ID, "node_id", input.Node.ID, "error", err)
			}
		},
	})
	if err != nil {
		return cleanup(err)
	}
	_, err = s.runtimeAuthority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: plan.Descriptor, Runtime: &runtimePlan, Workflow: &lease, Resource: sessionruntime.ReplaceAgentResource{},
		Ask: func(askCtx context.Context, scope sessionruntime.ExecutionScope, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
			return s.handleCurrentNodeAsk(askCtx, executionPromptAwaiter{authority: s.runtimeAuthority, scope: scope}, input, plan.Descriptor.SessionID().String(), askReq)
		},
		Runner: func(runCtx context.Context, scope sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			turnErr := bridge.WithEngine(runCtx, func(engineCtx context.Context, engine *runtime.Engine) error {
				if input.ContextMode == workflow.ContextModeCompactAndContinueSession {
					if err := engine.CompactContextForWorkflowContinuation(metadata.WithQueryFailureDiagnostics(engineCtx)); err != nil {
						return err
					}
				}
				_, err := engine.SubmitWorkflowTurn(metadata.WithQueryFailureDiagnostics(engineCtx))
				return err
			})
			if turnErr == nil {
				return nil
			}
			reason := ReasonRuntimeFailed
			if errors.Is(turnErr, context.Canceled) || context.Cause(runCtx) != nil {
				reason = ReasonRuntimeCanceled
			}
			return errors.Join(turnErr, s.failCurrentNodeScope(context.WithoutCancel(runCtx), controller, scope, reason, turnErr))
		},
	})
	if err != nil {
		return cleanup(err)
	}
	return nil
}

func (s *Starter) planCurrentNodeSession(ctx context.Context, input workflowstore.CurrentNodeStartContext, root workflowstore.ExecutionRoot) (launch.SessionPlan, bool, error) {
	cfg := s.cfg
	cfg.WorkspaceRoot = root.SourceWorkspaceRoot
	containerDir := filepath.Join(cfg.PersistenceRoot, "projects", input.Task.ProjectID, "sessions")
	intent, disposable, err := s.currentNodeSessionIntent(input, containerDir)
	if err != nil {
		return launch.SessionPlan{}, false, err
	}
	planner := launch.Planner{Config: cfg, ContainerDir: containerDir, StoreOptions: s.storeOptions, PersistedSessions: s.metadata, MetadataStoreOpener: func(string) (launch.MetadataExecutionTargetStore, error) { return s.metadata, nil }}
	plan, err := planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, Intent: intent, SkipContinuationAgentRoleValidation: workflowPromptOverrides(input.Node.SubagentRole).HasAny()})
	if err != nil {
		return launch.SessionPlan{}, disposable, err
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error { return store.EnsureDurable() }); err != nil {
		return launch.SessionPlan{}, disposable, err
	}
	if input.ContextMode == workflow.ContextModeCompactAndContinueSession {
		if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
			return store.ResetLockedContractForCompactionBoundary()
		}); err != nil {
			return launch.SessionPlan{}, disposable, err
		}
		plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, Intent: serverapi.OpenExistingSessionLaunchIntent(plan.Descriptor.SessionID()), SkipContinuationAgentRoleValidation: workflowPromptOverrides(input.Node.SubagentRole).HasAny()})
		if err != nil {
			return launch.SessionPlan{}, disposable, err
		}
	}
	plan, _, err = applyWorkflowSessionPromptOverridesForRole(plan, input.Node.SubagentRole)
	return plan, disposable, err
}

func (s *Starter) currentNodeSessionIntent(input workflowstore.CurrentNodeStartContext, containerDir string) (serverapi.SessionLaunchIntent, bool, error) {
	if input.CurrentNode.SessionID == nil {
		return serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), true, nil
	}
	if input.IsFanoutBranch && (input.ContextMode == workflow.ContextModeContinueSession || input.ContextMode == workflow.ContextModeCompactAndContinueSession) {
		id, err := s.cloneSourceSessionForFanout(containerDir, input.CurrentNode.SessionID.String())
		if err != nil {
			return serverapi.SessionLaunchIntent{}, false, err
		}
		sessionID, err := runtimeids.ParseSessionID(id)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, false, err
		}
		return serverapi.OpenExistingSessionLaunchIntent(sessionID), true, nil
	}
	return serverapi.OpenExistingSessionLaunchIntent(*input.CurrentNode.SessionID), false, nil
}

func (s *Starter) applyCurrentNodeSessionMetadata(ctx context.Context, input workflowstore.CurrentNodeStartContext, plan *launch.SessionPlan) error {
	name, err := workflowSessionNameFromCurrentNode(input)
	if err != nil {
		return err
	}
	preview, err := renderCurrentNodePrompt(input.PromptTemplate, input)
	if err != nil {
		return err
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error { return store.SetListingMetadata(name, preview) }); err != nil {
		return err
	}
	plan.SessionName, plan.FirstPromptPreview = &name, preview
	return nil
}

func (s *Starter) applyCurrentNodeSessionExecutionTarget(ctx context.Context, input workflowstore.CurrentNodeStartContext, descriptor session.SessionDescriptor) error {
	root, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return err
	}
	update := metadata.SessionExecutionTargetUpdate{SessionID: descriptor.SessionID().String(), Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: root.SourceWorkspaceID}, CwdRelpath: "."}
	if root.Managed != nil {
		update.Worktree = &metadata.SessionExecutionTargetUpdateWorktree{ID: root.Managed.WorktreeID}
	}
	return s.mutationPermit.Run(ctx, func(ctx context.Context) error { return s.metadata.UpdateSessionExecutionTarget(ctx, update) })
}

func currentNodeManagedWorktreePathContext(plan launch.SessionPlan, root workflowstore.ExecutionRoot) (*askquestion.ManagedWorktreePathContext, error) {
	if root.Managed == nil || strings.TrimSpace(plan.ActiveSettings.Worktrees.BaseDir) == "" {
		return nil, nil
	}
	return askquestion.NewManagedWorktreePathContext(plan.ActiveSettings.Worktrees.BaseDir, &root.Managed.Root)
}

func workflowSessionNameFromCurrentNode(input workflowstore.CurrentNodeStartContext) (string, error) {
	taskID := input.Task.ShortID
	if taskID == "" {
		taskID = string(input.Task.ID)
	}
	if taskID == "" || input.AcceptedTransitionPath.SourceNodeDisplayName == "" || input.AcceptedTransitionPath.TargetNodeDisplayName == "" {
		return "", errors.New("current workflow session metadata is incomplete")
	}
	return fmt.Sprintf("%s: %s -> %s", taskID, input.AcceptedTransitionPath.SourceNodeDisplayName, input.AcceptedTransitionPath.TargetNodeDisplayName), nil
}

func renderCurrentNodePrompt(text string, input workflowstore.CurrentNodeStartContext) (string, error) {
	source := ""
	if input.SourceSessionID != nil {
		source = input.SourceSessionID.String()
	}
	return renderWorkflowPrompt(text, workflowPromptInput{Task: input.Task, Workflow: input.Workflow, Node: input.Node, ContextMode: input.ContextMode, SourceSessionID: source, TransitionOptions: input.TransitionOptions, TransitionIDs: input.TransitionIDs, PromptTemplate: text, ParameterValues: input.ParameterValues, PriorParameterValues: input.PriorParameterValues})
}

func (s *Starter) resolveCurrentNodeCompletionMode(ctx context.Context, input workflowstore.CurrentNodeStartContext, plan launch.SessionPlan, client llm.Client) (workflowruntime.CompletionMode, llm.Client, error) {
	configured := s.cfg.Settings.Workflow.CompletionMode
	if input.Node.CompletionMode != "" {
		configured = config.WorkflowCompletionMode(input.Node.CompletionMode)
	}
	selection := workflowruntime.CompletionModeSelection{ConfiguredMode: configured, HasContinueSessionEdge: input.ContextMode == workflow.ContextModeContinueSession || input.ContextMode == workflow.ContextModeCompactAndContinueSession, ShellAvailable: toolIDEnabled(plan.EnabledTools, toolspec.ToolExecCommand)}
	if workflowCompletionModeNeedsProviderCapabilities(selection) {
		caps, resolved, err := s.workflowProviderCapabilities(ctx, plan, client)
		if err != nil {
			return "", resolved, err
		}
		selection.ProviderCapabilities, client = caps, resolved
	}
	mode, err := workflowruntime.SelectCompletionMode(selection)
	return mode, client, err
}

func (s *Starter) withSessionStore(ctx context.Context, descriptor session.SessionDescriptor, callback func(context.Context, *session.Store) error) error {
	return s.runtimeAuthority.WithSessionStore(ctx, descriptor, callback)
}

func (s *Starter) cleanupSession(ctx context.Context, descriptor session.SessionDescriptor) error {
	return errors.Join(s.withSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error { return store.RemoveDurable() }), s.metadata.DeleteSessionRecordByID(ctx, descriptor.SessionID().String()))
}

func (s *Starter) Close() error {
	if s == nil || s.closed.Swap(true) {
		return nil
	}
	return s.runtimeAuthority.StopWorkflowExecutions(context.Background())
}

func workflowCompletionModeNeedsProviderCapabilities(selection workflowruntime.CompletionModeSelection) bool {
	return selection.ConfiguredMode == config.WorkflowCompletionModeStructuredOutput || ((selection.ConfiguredMode == config.WorkflowCompletionModeAuto || selection.ConfiguredMode == "") && selection.ShellAvailable && !selection.HasContinueSessionEdge)
}

func (s *Starter) workflowProviderCapabilities(ctx context.Context, plan launch.SessionPlan, client llm.Client) (llm.ProviderCapabilities, llm.Client, error) {
	if caps, ok := llm.ProviderCapabilitiesFromLockedOrOverride(plan.Locked, plan.ActiveSettings.ProviderCapabilities); ok {
		return caps, client, nil
	}
	if client == nil {
		next, err := s.newWorkflowProviderClient(ctx, plan)
		if err != nil {
			return llm.ProviderCapabilities{}, nil, err
		}
		client = next
	}
	provider, ok := client.(llm.ProviderCapabilitiesClient)
	if !ok {
		return llm.ProviderCapabilities{}, client, fmt.Errorf("provider capabilities are unavailable for client %T", client)
	}
	caps, err := provider.ProviderCapabilities(ctx)
	return caps, client, err
}

func (s *Starter) newWorkflowProviderClient(ctx context.Context, plan launch.SessionPlan) (llm.Client, error) {
	active := plan.ActiveSettings
	if s.runtimeClientFactory != nil {
		providerSettings := runtimewire.RuntimeClientProviderSettings{
			Model:               active.Model,
			ProviderOverride:    active.ProviderOverride,
			OpenAIBaseURL:       active.OpenAIBaseURL,
			ModelVerbosity:      active.ModelVerbosity,
			ProviderIdentifier:  active.ProviderIdentifier,
			Store:               active.Store,
			ContextWindowTokens: active.ModelContextWindow,
			Auth:                "inherit",
		}
		if caps, configured := llm.ProviderCapabilitiesFromLockedOrOverride(plan.Locked, active.ProviderCapabilities); configured {
			providerSettings.ProviderCapabilitiesOverride = &caps
		}
		client, err := s.runtimeClientFactory.NewRuntimeClient(ctx, runtimewire.RuntimeClientRequest{
			Purpose:          runtimewire.RuntimeClientPurposeWorkflow,
			SessionID:        plan.Descriptor.SessionID().String(),
			ActiveSettings:   active,
			EnabledTools:     append([]toolspec.ID(nil), plan.EnabledTools...),
			WorkspaceRoot:    plan.WorkspaceRoot,
			Sources:          maps.Clone(plan.Source.Sources),
			ProviderSettings: providerSettings,
		})
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, errors.New("runtime client factory returned nil workflow client")
		}
		return client, nil
	}
	var authProvider llm.AuthHeaderProvider
	if s.authManager != nil {
		authProvider = s.authManager
	}
	return llm.NewProviderClient(llm.ProviderClientOptions{Provider: llm.Provider(active.ProviderOverride), Model: active.Model, Auth: authProvider, HTTPClient: llm.NewHTTPClient(time.Duration(active.Timeouts.ModelRequestSeconds) * time.Second), OpenAIBaseURL: active.OpenAIBaseURL, ModelVerbosity: string(active.ModelVerbosity), ProviderIdentifier: &active.ProviderIdentifier, Store: active.Store, ContextWindowTokens: active.ModelContextWindow})
}

func workflowPromptOverrides(role string) serverapi.RunPromptOverrides {
	if workflow.IsDefaultAgentRole(role) {
		role = workflow.DefaultAgentRole
	}
	if strings.TrimSpace(role) == "" {
		return serverapi.RunPromptOverrides{}
	}
	return serverapi.RunPromptOverrides{AgentRole: &role}
}

func applyWorkflowSessionPromptOverridesForRole(plan launch.SessionPlan, roleName string) (launch.SessionPlan, []string, error) {
	overrides := workflowPromptOverrides(roleName)
	prepared, err := launch.PrepareRunPromptOverridesWithContext(config.App{WorkspaceRoot: plan.WorkspaceRoot, Settings: plan.BaseSettings, Source: plan.BaseSource}, overrides, auth.EmptyState(), launch.RunPromptPreparationContext{ModelLock: plan.Locked, ToolLock: plan.Locked, OmittedTarget: &launch.PreparedBaseTarget{Settings: plan.ActiveSettings, Source: plan.Source, EnabledTools: plan.EnabledTools}})
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	role, err := overrides.AgentRoleOverride()
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	if !role.Present || role.Default {
		if prepared.BaseTarget == nil {
			return launch.SessionPlan{}, nil, errors.New("prepared workflow base target is required")
		}
		plan.ActiveSettings, plan.Source, plan.EnabledTools = prepared.BaseTarget.Settings, prepared.BaseTarget.Source, append([]toolspec.ID(nil), prepared.BaseTarget.EnabledTools...)
		return plan, nil, nil
	}
	if prepared.NamedTarget == nil {
		return launch.SessionPlan{}, nil, errors.New("prepared workflow role target is required")
	}
	plan.ActiveSettings, plan.Source, plan.EnabledTools = prepared.NamedTarget.Settings, prepared.NamedTarget.Source, append([]toolspec.ID(nil), prepared.NamedTarget.EnabledTools...)
	if prepared.NamedTarget.Warning == nil {
		return plan, nil, nil
	}
	return plan, []string{*prepared.NamedTarget.Warning}, nil
}

func (s *Starter) cloneSourceSessionForFanout(containerDir, sourceSessionID string) (string, error) {
	id, err := runtimeids.ParseSessionID(sourceSessionID)
	if err != nil {
		return "", err
	}
	descriptor, err := session.NewScopedOpenSessionDescriptor(id, containerDir)
	if err != nil {
		return "", err
	}
	var cloneID string
	err = s.withSessionStore(context.Background(), descriptor, func(_ context.Context, source *session.Store) error {
		log, err := source.MaterializeEventLog()
		if err != nil {
			return err
		}
		clone, err := session.CloneSession(log, "", sessioncontract.SessionCategorySubagent)
		if err != nil {
			return err
		}
		cloneID = clone.Meta().SessionID
		return nil
	})
	return cloneID, err
}

func (s *Starter) validateRole(role string) error {
	if workflow.IsDefaultAgentRole(role) || config.LookupSubagentRole(s.cfg.Settings, strings.TrimSpace(role)).Status == config.SubagentRoleLookupPresent {
		return nil
	}
	return fmt.Errorf("workflow validation failed: [%s]", workflow.CodeAgentRoleMissing)
}

type executionPromptAwaiter struct {
	authority *sessionruntime.Authority
	scope     sessionruntime.ExecutionScope
}

func (a executionPromptAwaiter) AwaitPromptResponse(ctx context.Context, _ string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	return a.authority.AwaitPromptResponse(ctx, a.scope.ID(), req)
}

func currentNodeQuestionContext(input workflowstore.CurrentNodeStartContext, sessionID string) workflowattention.TaskQuestionContext {
	return workflowattention.TaskQuestionContext{Task: input.Task, CurrentNode: input.CurrentNode.Reference, SessionID: sessionID}
}

func (s *Starter) handleCurrentNodeAsk(ctx context.Context, awaiter workflowattention.QuestionAwaiter, input workflowstore.CurrentNodeStartContext, sessionID string, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	context := currentNodeQuestionContext(input, sessionID)
	if askReq.Approval {
		return workflowattention.HandleTaskApprovalQuestion(ctx, awaiter, s.attention, workflowattention.TaskQuestionRequest{Context: context, Question: askReq})
	}
	return workflowattention.HandleTaskQuestion(ctx, awaiter, s.attention, workflowattention.TaskQuestionRequest{Context: context, Question: askReq})
}

func workflowRuntimeEnabledTools(enabled []toolspec.ID) []toolspec.ID {
	return append([]toolspec.ID(nil), enabled...)
}

func toolIDEnabled(enabled []toolspec.ID, want toolspec.ID) bool {
	for _, id := range enabled {
		if id == want {
			return true
		}
	}
	return false
}

func BuildCurrentSessionTaskInstructions(input workflowstore.CurrentNodeStartContext) (workflowruntime.TaskInstructions, error) {
	source := ""
	if input.SourceSessionID != nil {
		source = input.SourceSessionID.String()
	}
	return buildWorkflowTaskInstructions(workflowPromptInput{Task: input.Task, Workflow: input.Workflow, Node: input.Node, ContextMode: input.ContextMode, SourceSessionID: source, TransitionOptions: input.TransitionOptions, TransitionIDs: input.TransitionIDs, PromptTemplate: input.PromptTemplate, ParameterValues: input.ParameterValues, PriorParameterValues: input.PriorParameterValues})
}

type workflowPromptInput struct {
	Task                 workflowstore.TaskRecord
	Workflow             workflowstore.WorkflowRecord
	Node                 workflowstore.NodeRecord
	ContextMode          workflow.ContextMode
	SourceSessionID      string
	TransitionOptions    []workflowstore.TransitionOption
	TransitionIDs        []string
	PromptTemplate       string
	ParameterValues      map[string]string
	PriorParameterValues map[string]map[string]string
}

func buildWorkflowTaskInstructions(input workflowPromptInput) (workflowruntime.TaskInstructions, error) {
	prompt, err := renderWorkflowPrompt(input.PromptTemplate, input)
	if err != nil {
		return workflowruntime.TaskInstructions{}, err
	}
	shortID := input.Task.ShortID
	if shortID == "" {
		shortID = string(input.Task.ID)
	}
	return workflowruntime.TaskInstructions{TaskID: string(input.Task.ID), TaskShortID: shortID, TaskTitle: input.Task.Title, TaskBody: input.Task.Body, WorkflowID: string(input.Task.WorkflowID), WorkflowShortID: string(input.Workflow.ID), NodeID: string(input.Node.ID), NodeKey: string(input.Node.Key), NodeDisplayName: input.Node.DisplayName, ContextMode: string(input.ContextMode), SourceSessionID: input.SourceSessionID, Transitions: workflowInstructionTransitions(input.TransitionOptions, input.TransitionIDs), NodePrompt: prompt}, nil
}

func workflowTransitions(options []workflowstore.TransitionOption, ids []string) []prompts.WorkflowTransition {
	out := make([]prompts.WorkflowTransition, 0, len(options))
	for _, option := range options {
		if strings.TrimSpace(option.ID) != "" {
			out = append(out, prompts.WorkflowTransition{ID: option.ID, DisplayName: option.DisplayName, Description: option.Description})
		}
	}
	if len(out) != 0 {
		return out
	}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, prompts.WorkflowTransition{ID: id})
		}
	}
	return out
}

func workflowInstructionTransitions(options []workflowstore.TransitionOption, ids []string) []workflowruntime.TransitionInstruction {
	transitions := workflowTransitions(options, ids)
	out := make([]workflowruntime.TransitionInstruction, 0, len(transitions))
	for _, transition := range transitions {
		out = append(out, workflowruntime.TransitionInstruction{ID: transition.ID, DisplayName: transition.DisplayName, Description: transition.Description})
	}
	return out
}

func workflowCompletionTransitions(options []workflowstore.TransitionOption, ids []string) []workflowruntime.CompletionTransition {
	out := make([]workflowruntime.CompletionTransition, 0, len(options))
	for _, option := range options {
		if strings.TrimSpace(option.ID) != "" {
			out = append(out, workflowruntime.CompletionTransition{ID: option.ID, DisplayName: option.DisplayName, Description: option.Description, Parameters: append([]workflow.Parameter(nil), option.Parameters...)})
		}
	}
	if len(out) != 0 {
		return out
	}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, workflowruntime.CompletionTransition{ID: id})
		}
	}
	return out
}

type nodePromptTemplateData struct {
	TaskId, TaskShortId, TaskTitle, TaskBody, NodeId, NodeKey, NodeDisplayName string
	Params                                                                     map[string]promptParameterNamespace
}

const currentParameterValueKey = "\x00current"

type promptParameterNamespace map[string]string

func (n promptParameterNamespace) String() string { return n[currentParameterValueKey] }

func renderWorkflowPrompt(text string, input workflowPromptInput) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	tmpl, err := template.New("workflow transition prompt").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse workflow transition prompt template: %w", err)
	}
	var out strings.Builder
	err = tmpl.Execute(&out, nodePromptTemplateData{TaskId: string(input.Task.ID), TaskShortId: input.Task.ShortID, TaskTitle: input.Task.Title, TaskBody: input.Task.Body, NodeId: string(input.Node.ID), NodeKey: string(input.Node.Key), NodeDisplayName: input.Node.DisplayName, Params: promptParameterData(input.ParameterValues, input.PriorParameterValues)})
	return out.String(), err
}

func promptParameterData(current map[string]string, prior map[string]map[string]string) map[string]promptParameterNamespace {
	out := map[string]promptParameterNamespace{workflow.RuntimePromptParameterCommentary: {currentParameterValueKey: ""}}
	for transition, values := range prior {
		namespace := out[transition]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		for key, value := range values {
			namespace[key] = value
		}
		out[transition] = namespace
	}
	for key, value := range current {
		namespace := out[key]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		namespace[currentParameterValueKey] = value
		out[key] = namespace
	}
	return out
}

var _ workflowexecution.CurrentNodeRunner = (*Starter)(nil)
