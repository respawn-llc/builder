package sessionruntime

import (
	"context"
	"errors"
	"maps"
	"os"
	"strings"

	"core/server/auth"
	"core/server/llm"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/toolspec"
	"core/shared/transcriptdiag"
)

type AgentRuntimePlanOptions struct {
	Settings                            config.Settings
	EnabledTools                        []toolspec.ID
	FilesystemContext                   tools.FilesystemContext
	Sources                             map[string]string
	Headless                            bool
	FastMode                            *runtime.FastModeState
	Client                              llm.Client
	ClientFactory                       runtimewire.RuntimeClientFactory
	ReviewerClientFactory               runtimewire.RuntimeClientFactory
	CurrentNodeExecution                *workflowruntime.CurrentNodeExecutionConfig
	AskQuestionBatchSkipped             func(tools.AskQuestionBatchMetadata)
	PromptFacingSnapshotReloader        runtime.PromptFacingSnapshotReloader
	ProviderCapabilitiesOverride        *llm.ProviderCapabilities
	SkipContinuationAgentRoleValidation bool
	OnEvent                             func(runtime.Event)
	OnLoggingFailure                    func(string)
	StartLogLines                       []string
	RecoveredWarningProvider            func() (string, bool, error)
}

type AgentRuntimePlan struct {
	options AgentRuntimePlanOptions
}

func NewAgentRuntimePlan(options AgentRuntimePlanOptions) (AgentRuntimePlan, error) {
	if strings.TrimSpace(options.FilesystemContext.Access.WorkingDirectory.LexicalPath) == "" ||
		strings.TrimSpace(options.FilesystemContext.Access.WorkingDirectory.RealPath) == "" ||
		strings.TrimSpace(options.FilesystemContext.Access.ExecutionTargetRoot.LexicalPath) == "" ||
		strings.TrimSpace(options.FilesystemContext.Access.ExecutionTargetRoot.RealPath) == "" {
		return AgentRuntimePlan{}, errors.New("agent runtime filesystem context is required")
	}
	options.Settings = cloneAgentRuntimeSettings(options.Settings)
	options.EnabledTools = append([]toolspec.ID(nil), options.EnabledTools...)
	options.Sources = maps.Clone(options.Sources)
	options.StartLogLines = append([]string(nil), options.StartLogLines...)
	options.FilesystemContext = options.FilesystemContext.Clone()
	if options.ProviderCapabilitiesOverride != nil {
		value := *options.ProviderCapabilitiesOverride
		options.ProviderCapabilitiesOverride = &value
	}
	return AgentRuntimePlan{options: options}, nil
}

func cloneAgentRuntimeSettings(settings config.Settings) config.Settings {
	cloned := settings
	cloned.SystemPromptFiles = append([]config.SystemPromptFile(nil), settings.SystemPromptFiles...)
	cloned.EnabledTools = maps.Clone(settings.EnabledTools)
	cloned.SkillToggles = maps.Clone(settings.SkillToggles)
	cloned.Shell.PostprocessHook = cloneStringPointer(settings.Shell.PostprocessHook)
	if settings.Subagents != nil {
		cloned.Subagents = make(map[string]config.SubagentRole, len(settings.Subagents))
		for name, role := range settings.Subagents {
			copied := role
			copied.Settings = cloneAgentRuntimeSettings(role.Settings)
			copied.Sources = maps.Clone(role.Sources)
			cloned.Subagents[name] = copied
		}
	}
	return cloned
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type authorityRuntimeOptions struct {
	persistenceRoot   string
	authManager       *auth.Manager
	background        *shelltool.Manager
	storeOptions      []session.StoreOption
	eventFeed         AgentResourceEventFeed
	resourceLifecycle AgentResourceLifecycle
	stepLifecycle     AgentResourceStepLifecycle
}

func newAuthorityRuntimeOptions(options AuthorityOptions) authorityRuntimeOptions {
	return authorityRuntimeOptions{
		persistenceRoot:   options.PersistenceRoot,
		authManager:       options.AuthManager,
		background:        options.Background,
		storeOptions:      append([]session.StoreOption(nil), options.StoreOptions...),
		eventFeed:         options.EventFeed,
		resourceLifecycle: options.ResourceLifecycle,
		stepLifecycle:     options.StepLifecycle,
	}
}

func (a *Authority) buildAgentResource(ctx context.Context, descriptor session.SessionDescriptor, plan *AgentRuntimePlan) (*agentResource, error) {
	if a.options.persistenceRoot == "" {
		return nil, errors.New("authority persistence root is required")
	}
	sessionID := descriptor.SessionID()
	durabilityObserver := runlog.NewDurabilityObserver()
	storeOptions := append(
		append([]session.StoreOption(nil), a.options.storeOptions...),
		session.WithDurabilityObserver(durabilityObserver),
	)
	store, err := session.MaterializeSessionDescriptor(a.options.persistenceRoot, descriptor, storeOptions...)
	if err != nil {
		return nil, err
	}
	if err := store.EnsureDurable(); err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("agent runtime plan is required")
	}
	if err := appendRecoveredWarning(store, plan.options.RecoveredWarningProvider); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.nextResource++
	generation := a.nextResource
	if generation == 0 {
		a.mu.Unlock()
		panic("session runtime resource generation overflow")
	}
	a.mu.Unlock()
	ref, err := runtimeids.NewSessionResourceRef(sessionID, generation)
	if err != nil {
		return nil, err
	}
	resourceContext, cancel := context.WithCancel(context.Background())
	resource := &agentResource{
		authority:            a,
		ref:                  ref,
		ctx:                  resourceContext,
		cancel:               cancel,
		changed:              make(chan struct{}),
		state:                AgentResourceBuilding,
		owners:               make(map[string]struct{}),
		ownerlessDisposition: agentResourceRemainAvailable,
		store:                store,
	}
	logger, err := runlog.NewRunLogger(store.Dir(), func(diag runlog.RunLoggerDiagnostic) {
		if plan.options.OnLoggingFailure != nil {
			plan.options.OnLoggingFailure(diag.Message)
		}
	})
	if err != nil {
		cancel()
		return nil, err
	}
	durabilityObserver.Attach(logger)
	for _, line := range plan.options.StartLogLines {
		logger.Logf("%s", line)
	}
	wiring, err := a.newRuntimeWiringFromPlan(resource, store, logger, durabilityObserver, *plan)
	if err != nil {
		cancel()
		err = errors.Join(err, logger.Close())
		return nil, err
	}
	if wiring == nil || wiring.Engine == nil {
		cancel()
		_ = logger.Close()
		return nil, errors.New("agent runtime factory returned no engine")
	}
	resource.mu.Lock()
	resource.engine = wiring.Engine
	resource.eventBridge = wiring.EventBridge
	resource.logger = logger
	resource.localTools = wiring.LocalTools
	resource.askBroker = wiring.AskBroker
	resource.backgroundLimit = plan.options.Settings.ShellOutputMaxChars
	resource.backgroundMode = shelltool.NormalizeBackgroundOutputMode(string(plan.options.Settings.BGShellsOutput))
	resource.close = func() error {
		return errors.Join(wiring.Close(), logger.Close())
	}
	resource.state = AgentResourceReady
	resource.signalLocked()
	resource.mu.Unlock()
	return resource, nil
}

func (a *Authority) newRuntimeWiringFromPlan(resource *agentResource, store *session.Store, logger *runlog.RunLogger, durabilityObserver *runlog.DurabilityObserver, plan AgentRuntimePlan) (*runtimewire.RuntimeWiring, error) {
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return nil, err
	}
	options := plan.options
	wiringOptions := runtimewire.RuntimeWiringOptions{
		Context:                             resource.ctx,
		Headless:                            options.Headless,
		FastMode:                            options.FastMode,
		Sources:                             maps.Clone(options.Sources),
		Client:                              options.Client,
		ClientFactory:                       options.ClientFactory,
		ReviewerClientFactory:               options.ReviewerClientFactory,
		CurrentNodeExecution:                options.CurrentNodeExecution,
		AskQuestionBatchSkipped:             options.AskQuestionBatchSkipped,
		PromptFacingSnapshotReloader:        options.PromptFacingSnapshotReloader,
		ProviderCapabilitiesOverride:        options.ProviderCapabilitiesOverride,
		SkipContinuationAgentRoleValidation: options.SkipContinuationAgentRoleValidation,
		GlobalConfigDir:                     a.options.persistenceRoot,
		FilesystemContext:                   options.FilesystemContext,
		StepLifecycle:                       resource,
		DurabilityObserver:                  durabilityObserver,
		LifecycleTaskFinished: func() error {
			return a.closeRetiringResource(context.Background(), resource)
		},
		LifecycleRuntimeAbort: func() error {
			return a.retireRuntimeAbortResource(context.Background(), resource)
		},
		OnEvent: func(event runtime.Event) {
			logger.Logf("%s", runlog.FormatRuntimeEvent(event))
			if transcriptdiag.Enabled(options.Settings.Debug, os.Getenv) {
				logger.Logf("%s", runlog.FormatTranscriptRuntimeEventDiagnostic(resource.ref.SessionID().String(), event))
			}
			if a.options.eventFeed != nil {
				a.options.eventFeed(resource.ref, event)
			}
			if options.OnEvent != nil {
				options.OnEvent(event)
			}
		},
	}
	return runtimewire.NewRuntimeWiringWithBackground(
		store,
		eventLog,
		options.Settings,
		options.EnabledTools,
		a.options.authManager,
		logger,
		a.options.background,
		wiringOptions,
	)
}
