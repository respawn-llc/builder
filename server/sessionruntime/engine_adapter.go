package sessionruntime

import (
	"context"
	"errors"
	"fmt"
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

type AgentRuntimeFactory func(context.Context, *session.Store, AgentResourceDescriptor) (*runtimewire.RuntimeWiring, error)

type AgentRuntimePlanOptions struct {
	Settings                            config.Settings
	EnabledTools                        []toolspec.ID
	Workdir                             string
	Sources                             map[string]string
	Headless                            bool
	FastMode                            *runtime.FastModeState
	Client                              llm.Client
	ClientFactory                       runtimewire.RuntimeClientFactory
	ReviewerClientFactory               runtimewire.RuntimeClientFactory
	WorkflowRun                         *workflowruntime.Config
	AskQuestionBatchSkipped             func(tools.AskQuestionBatchMetadata)
	PromptFacingSnapshotReloader        runtime.PromptFacingSnapshotReloader
	ProviderCapabilitiesOverride        *llm.ProviderCapabilities
	SkipContinuationAgentRoleValidation bool
	OnEvent                             func(runtime.Event)
	OnLoggingFailure                    func(string)
	StartLogLines                       []string
}

type AgentRuntimePlan struct {
	options AgentRuntimePlanOptions
}

func NewAgentRuntimePlan(options AgentRuntimePlanOptions) (AgentRuntimePlan, error) {
	options.Workdir = strings.TrimSpace(options.Workdir)
	if options.Workdir == "" {
		return AgentRuntimePlan{}, errors.New("agent runtime workdir is required")
	}
	options.Settings = cloneAgentRuntimeSettings(options.Settings)
	options.EnabledTools = append([]toolspec.ID(nil), options.EnabledTools...)
	options.Sources = maps.Clone(options.Sources)
	options.StartLogLines = append([]string(nil), options.StartLogLines...)
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
	persistenceRoot string
	authManager     *auth.Manager
	background      *shelltool.Manager
	storeOptions    []session.StoreOption
	wiring          runtimewire.RuntimeWiringOptions
	runtimeFactory  AgentRuntimeFactory
	eventFeed       AgentResourceEventFeed
	stepLifecycle   AgentResourceStepLifecycle
}

func newAuthorityRuntimeOptions(options AuthorityOptions) authorityRuntimeOptions {
	return authorityRuntimeOptions{
		persistenceRoot: options.PersistenceRoot,
		authManager:     options.AuthManager,
		background:      options.Background,
		storeOptions:    append([]session.StoreOption(nil), options.StoreOptions...),
		wiring:          cloneRuntimeWiringOptions(options.RuntimeWiring),
		runtimeFactory:  options.RuntimeFactory,
		eventFeed:       options.EventFeed,
		stepLifecycle:   options.StepLifecycle,
	}
}

func cloneRuntimeWiringOptions(options runtimewire.RuntimeWiringOptions) runtimewire.RuntimeWiringOptions {
	cloned := options
	cloned.Sources = cloneStringMap(options.Sources)
	if options.ProviderCapabilitiesOverride != nil {
		value := *options.ProviderCapabilitiesOverride
		cloned.ProviderCapabilitiesOverride = &value
	}
	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (a *Authority) buildAgentResource(ctx context.Context, descriptor session.SessionDescriptor, plan *AgentRuntimePlan) (*agentResource, error) {
	if a.options.persistenceRoot == "" {
		return nil, errors.New("authority persistence root is required")
	}
	sessionID := descriptor.SessionID()
	store, err := session.MaterializeSessionDescriptor(a.options.persistenceRoot, descriptor, a.options.storeOptions...)
	if err != nil {
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
		authority: a,
		ref:       ref,
		ctx:       resourceContext,
		cancel:    cancel,
		changed:   make(chan struct{}),
		state:     AgentResourceBuilding,
		owners:    make(map[string]struct{}),
		store:     store,
	}
	resourceDescriptor := resource.descriptor()
	var wiring *runtimewire.RuntimeWiring
	var logger *runlog.RunLogger
	if plan != nil {
		logger, err = runlog.NewRunLogger(store.Dir(), func(diag runlog.RunLoggerDiagnostic) {
			if plan.options.OnLoggingFailure != nil {
				plan.options.OnLoggingFailure(diag.Message)
			}
		})
		if err == nil {
			for _, line := range plan.options.StartLogLines {
				logger.Logf("%s", line)
			}
			wiring, err = a.newRuntimeWiringFromPlan(resource, store, logger, *plan)
		}
	} else if a.options.runtimeFactory != nil {
		wiring, err = a.options.runtimeFactory(ctx, store, resourceDescriptor)
	} else {
		wiring, err = a.newRuntimeWiring(resource, store)
	}
	if err != nil {
		cancel()
		if logger != nil {
			err = errors.Join(err, logger.Close())
		}
		return nil, err
	}
	if wiring == nil || wiring.Engine == nil {
		cancel()
		if logger != nil {
			_ = logger.Close()
		}
		return nil, errors.New("agent runtime factory returned no engine")
	}
	resource.mu.Lock()
	resource.engine = wiring.Engine
	resource.eventBridge = wiring.EventBridge
	resource.logger = logger
	resource.localTools = wiring.LocalTools
	resource.askBroker = wiring.AskBroker
	resource.close = wiring.Close
	if logger != nil {
		resource.close = func() error {
			return errors.Join(wiring.Close(), logger.Close())
		}
	}
	resource.state = AgentResourceReady
	resource.signalLocked()
	resource.mu.Unlock()
	return resource, nil
}

func (a *Authority) newRuntimeWiringFromPlan(resource *agentResource, store *session.Store, logger *runlog.RunLogger, plan AgentRuntimePlan) (*runtimewire.RuntimeWiring, error) {
	options := plan.options
	wiringOptions := runtimewire.RuntimeWiringOptions{
		Context:                             resource.ctx,
		Headless:                            options.Headless,
		FastMode:                            options.FastMode,
		Sources:                             maps.Clone(options.Sources),
		Client:                              options.Client,
		ClientFactory:                       options.ClientFactory,
		ReviewerClientFactory:               options.ReviewerClientFactory,
		WorkflowRun:                         options.WorkflowRun,
		AskQuestionBatchSkipped:             options.AskQuestionBatchSkipped,
		PromptFacingSnapshotReloader:        options.PromptFacingSnapshotReloader,
		ProviderCapabilitiesOverride:        options.ProviderCapabilitiesOverride,
		SkipContinuationAgentRoleValidation: options.SkipContinuationAgentRoleValidation,
		GlobalConfigDir:                     a.options.persistenceRoot,
		StepLifecycle:                       resource,
		OnEvent: func(event runtime.Event) {
			logger.Logf("%s", runlog.FormatRuntimeEvent(event))
			if transcriptdiag.Enabled(options.Settings.Debug, os.Getenv) {
				logger.Logf("%s", runlog.FormatTranscriptRuntimeEventDiagnostic(resource.ref.SessionID().String(), event))
			}
			if a.options.eventFeed != nil {
				a.options.eventFeed(resource.descriptor(), event)
			}
			if options.OnEvent != nil {
				options.OnEvent(event)
			}
		},
	}
	return runtimewire.NewRuntimeWiringWithBackground(
		store,
		options.Settings,
		options.EnabledTools,
		options.Workdir,
		a.options.authManager,
		logger,
		a.options.background,
		wiringOptions,
	)
}

func (a *Authority) newRuntimeWiring(resource *agentResource, store *session.Store) (*runtimewire.RuntimeWiring, error) {
	workspaceRoot := store.Meta().WorkspaceRoot
	app, err := config.Load(workspaceRoot, config.LoadOptions{ConfigRoot: a.options.persistenceRoot})
	if err != nil {
		return nil, fmt.Errorf("load agent runtime config: %w", err)
	}
	options := cloneRuntimeWiringOptions(a.options.wiring)
	options.Context = resource.ctx
	options.OnEvent = func(event runtime.Event) {
		if a.options.eventFeed != nil {
			a.options.eventFeed(resource.descriptor(), event)
		}
	}
	options.StepLifecycle = resource
	enabledTools := append([]toolspec.ID(nil), config.EnabledToolIDs(app.Settings)...)
	return runtimewire.NewRuntimeWiringWithBackground(
		store,
		app.Settings,
		enabledTools,
		workspaceRoot,
		a.options.authManager,
		nil,
		a.options.background,
		options,
	)
}
