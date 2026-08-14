package runtimewire

import (
	"context"
	"fmt"
	"strings"
	"time"

	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	askquestion "core/server/tools"
	triggerhandofftool "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type RuntimeWiring struct {
	Engine        *runtime.Engine
	AskBroker     *askquestion.AskQuestionBroker
	EventBridge   *EventBridge
	Background    *shelltool.Manager
	LocalTools    *LocalToolRegistryBinding
	PromptHistory []string
}

func (w *RuntimeWiring) Close() error {
	if w == nil || w.Engine == nil {
		return nil
	}
	return w.Engine.Close()
}

type RuntimeWiringOptions struct {
	FilesystemContext                   tools.FilesystemContext
	Context                             context.Context
	OnEvent                             func(evt runtime.Event)
	Headless                            bool
	FastMode                            *runtime.FastModeState
	QuestionsEnabled                    *bool
	AutoCompactionEnabled               *bool
	Sources                             map[string]string
	Client                              llm.Client
	ClientFactory                       RuntimeClientFactory
	ReviewerClientFactory               RuntimeClientFactory
	WorkflowPrompt                      *workflowruntime.PromptContract
	AskQuestionBatchSkipped             func(askquestion.AskQuestionBatchMetadata)
	PromptFacingSnapshotReloader        runtime.PromptFacingSnapshotReloader
	ProviderCapabilitiesOverride        *llm.ProviderCapabilities
	SkipContinuationAgentRoleValidation bool
	StepLifecycle                       runtime.StepLifecycleSink
	LifecycleTaskFinished               func() error
	LifecycleRuntimeAbort               func() error
	DurabilityObserver                  runtime.ResultGroupDurabilityObserver
	ApplyWorktreeTarget                 func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error
	// GlobalConfigDir is the absolute persistence root that owns model-visible
	// global context (AGENTS.md, system prompt, skills). Empty falls back to
	// ~/.kent inside the runtime resolvers.
	GlobalConfigDir string
}

func NewRuntimeWiring(store *session.Store, eventLog session.MaterializedEventLog, active config.Settings, enabledTools []toolspec.ID, mgr *auth.Manager, logger Logger, opts RuntimeWiringOptions) (*RuntimeWiring, error) {
	return NewRuntimeWiringWithBackground(store, eventLog, active, enabledTools, mgr, logger, nil, opts)
}

func NewRuntimeWiringWithBackground(
	store *session.Store,
	eventLog session.MaterializedEventLog,
	active config.Settings,
	enabledTools []toolspec.ID,
	mgr *auth.Manager,
	logger Logger,
	background *shelltool.Manager,
	opts RuntimeWiringOptions,
) (*RuntimeWiring, error) {
	if opts.Client != nil && opts.ClientFactory != nil {
		return nil, ErrRuntimeClientFactoryConflict
	}
	shellPostprocessor, err := postprocess.NewRunner(postprocess.Settings{
		Mode:     active.Shell.PostprocessingMode,
		HookPath: active.Shell.PostprocessHook,
	})
	if err != nil {
		return nil, fmt.Errorf("compile effective shell postprocessor: %w", err)
	}
	filesystemContext := opts.FilesystemContext.Clone()
	if err := validateFilesystemContext(filesystemContext); err != nil {
		return nil, err
	}
	workingDirectory := filesystemContext.Access.WorkingDirectory.LexicalPath
	var eng *runtime.Engine
	localTools, askBroker, background, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:        filesystemContext,
		OwnerSessionID:           store.Meta().SessionID,
		Enabled:                  enabledTools,
		MinimumExecToBgTime:      time.Duration(active.MinimumExecToBgSeconds) * time.Second,
		ShellOutputMaxChars:      active.ShellOutputMaxChars,
		AllowNonCwdEdits:         active.AllowNonCwdEdits,
		SupportsVision:           llm.LockedContractSupportsVisionInputs(store.Meta().Locked, active.Model),
		Logger:                   logger,
		Background:               background,
		ShellPostprocessor:       shellPostprocessor,
		GlobalConfigDir:          opts.GlobalConfigDir,
		TriggerHandoffController: func() triggerhandofftool.TriggerHandoffController { return eng },
		QuestionsEnabledGetter: func() bool {
			if eng == nil {
				return true
			}
			return eng.QuestionsEnabled()
		},
	})
	if err != nil {
		return nil, err
	}
	toolRegistry := localTools.Registry()
	factoryContext := opts.Context
	if factoryContext == nil {
		factoryContext = context.Background()
	}

	mainProvider := mainProviderRuntimeSettings(active)
	if resolvedCapabilities, ok := llm.ProviderCapabilitiesFromLockedOrOverride(store.Meta().Locked, active.ProviderCapabilities); ok {
		mainProvider.ProviderCapabilitiesOverride = &resolvedCapabilities
	}
	var client llm.Client
	if opts.Client != nil {
		client = opts.Client
	} else if opts.ClientFactory != nil {
		client, err = newRuntimeClientFromFactory(factoryContext, opts.ClientFactory, RuntimeClientPurposeMain, store.Meta().SessionID, active, enabledTools, filesystemContext.Access.WorkingDirectory.LexicalPath, opts.Sources, mainProvider)
		if err != nil {
			return nil, err
		}
	} else {
		var mainAuth llm.AuthHeaderProvider
		if mgr != nil && !strings.EqualFold(strings.TrimSpace(mainProvider.Auth), "none") {
			mainAuth = mgr
		}
		client, err = llm.NewProviderClient(llm.ProviderClientOptions{
			Provider:                     llm.Provider(strings.TrimSpace(mainProvider.ProviderOverride)),
			Model:                        mainProvider.Model,
			Auth:                         mainAuth,
			HTTPClient:                   llm.NewProviderHTTPClient(mainProvider.OpenAIBaseURL, time.Duration(active.Timeouts.ModelRequestSeconds)*time.Second),
			OpenAIBaseURL:                mainProvider.OpenAIBaseURL,
			ModelVerbosity:               string(mainProvider.ModelVerbosity),
			ProviderIdentifier:           &mainProvider.ProviderIdentifier,
			Store:                        mainProvider.Store,
			ContextWindowTokens:          mainProvider.ContextWindowTokens,
			ProviderCapabilitiesOverride: mainProvider.ProviderCapabilitiesOverride,
		})
		if err != nil {
			return nil, err
		}
	}

	reviewerProvider := reviewerProviderRuntimeSettings(active)
	newReviewerClient := func() (llm.Client, error) {
		if opts.ClientFactory != nil {
			return newRuntimeClientFromFactory(factoryContext, opts.ClientFactory, RuntimeClientPurposeReviewer, store.Meta().SessionID, active, enabledTools, workingDirectory, opts.Sources, reviewerProvider)
		}
		if opts.ReviewerClientFactory != nil {
			return newRuntimeClientFromFactory(factoryContext, opts.ReviewerClientFactory, RuntimeClientPurposeReviewer, store.Meta().SessionID, active, enabledTools, workingDirectory, opts.Sources, reviewerProvider)
		}
		var reviewerAuth llm.AuthHeaderProvider
		if mgr != nil && !strings.EqualFold(strings.TrimSpace(reviewerProvider.Auth), "none") {
			reviewerAuth = mgr
		}
		return llm.NewProviderClient(llm.ProviderClientOptions{
			Provider:                     llm.Provider(strings.TrimSpace(reviewerProvider.ProviderOverride)),
			Model:                        reviewerProvider.Model,
			Auth:                         reviewerAuth,
			HTTPClient:                   llm.NewProviderHTTPClient(reviewerProvider.OpenAIBaseURL, time.Duration(active.Reviewer.TimeoutSeconds)*time.Second),
			OpenAIBaseURL:                reviewerProvider.OpenAIBaseURL,
			ModelVerbosity:               string(reviewerProvider.ModelVerbosity),
			ProviderIdentifier:           &reviewerProvider.ProviderIdentifier,
			Store:                        reviewerProvider.Store,
			ContextWindowTokens:          reviewerProvider.ContextWindowTokens,
			ProviderCapabilitiesOverride: reviewerProvider.ProviderCapabilitiesOverride,
		})
	}

	var reviewerClient llm.Client
	if strings.ToLower(strings.TrimSpace(active.Reviewer.Frequency)) != "off" {
		reviewerClient, err = newReviewerClient()
		if err != nil {
			return nil, err
		}
	}

	eventBridge := NewEventBridge(2048, func(total uint64, evt runtime.Event) {
		if logger == nil {
			return
		}
		if total == 1 || total%100 == 0 {
			logger.Logf("runtime.event.drop count=%d kind=%s", total, evt.Kind)
		}
	})
	promptReloader := opts.PromptFacingSnapshotReloader
	if promptReloader == nil {
		promptReloader = launchPromptFacingSnapshotReloader{
			store:                               store,
			workspaceRoot:                       workingDirectory,
			configRoot:                          opts.GlobalConfigDir,
			skipContinuationAgentRoleValidation: opts.SkipContinuationAgentRoleValidation,
		}
	}
	providerCapabilitiesOverride := mainProvider.ProviderCapabilitiesOverride
	if opts.ProviderCapabilitiesOverride != nil {
		providerCapabilitiesOverride = opts.ProviderCapabilitiesOverride
	}
	eng, err = runtime.New(store, eventLog, client, toolRegistry, runtime.Config{
		Model:                           active.Model,
		Debug:                           active.Debug,
		Temperature:                     1,
		MaxTokens:                       0,
		ThinkingLevel:                   active.ThinkingLevel,
		ModelCapabilities:               llm.LockedModelCapabilitiesForConfig(active.Model, active.ModelCapabilities),
		FastModeEnabled:                 active.PriorityRequestMode,
		FastModeState:                   opts.FastMode,
		WebSearchMode:                   active.WebSearch,
		PromptFacingSnapshotReloader:    promptReloader,
		ProviderCapabilitiesOverride:    providerCapabilitiesOverride,
		EnabledTools:                    enabledTools,
		SkillPolicy:                     config.ResolveSkillPolicy(active),
		SubagentCatalogSettings:         active,
		SystemPromptFiles:               active.SystemPromptFiles,
		AutoCompactTokenLimit:           active.ContextCompactionThresholdTokens,
		PreSubmitCompactionLeadTokens:   active.PreSubmitCompactionLeadTokens,
		ContextWindowTokens:             active.ModelContextWindow,
		EffectiveContextWindowPercent:   95,
		LocalCompactionCarryoverLimit:   20_000,
		CompactionMode:                  string(active.CompactionMode),
		CacheWarningMode:                active.CacheWarningMode,
		AutoCompactionEnabled:           textutil.Pointer(opts.AutoCompactionEnabled),
		QuestionsEnabled:                textutil.Pointer(opts.QuestionsEnabled),
		HeadlessMode:                    opts.Headless,
		ToolPreambles:                   active.ToolPreambles,
		WorkflowPrompt:                  opts.WorkflowPrompt,
		AskQuestionBatchSkipped:         opts.AskQuestionBatchSkipped,
		TranscriptWorkingDir:            workingDirectory,
		GlobalConfigDir:                 opts.GlobalConfigDir,
		WorkflowPreCompactionTokenLimit: config.ResolveWorkflowPreCompactionTokens(active),
		Reviewer: runtime.ReviewerConfig{
			Frequency:         active.Reviewer.Frequency,
			Model:             active.Reviewer.Model,
			ThinkingLevel:     active.Reviewer.ThinkingLevel,
			ModelCapabilities: lockedModelCapabilitiesForConfig(active.Reviewer.Model, active.Reviewer.ModelCapabilities, opts.Sources, "reviewer.model_capabilities.supports_reasoning_effort", "reviewer.model_capabilities.supports_vision_inputs"),
			SystemPromptFile:  active.Reviewer.SystemPromptFile,
			VerboseOutput:     active.Reviewer.VerboseOutput,
			Client:            reviewerClient,
			ClientFactory:     newReviewerClient,
		},
		OnEvent: func(evt runtime.Event) {
			if opts.OnEvent != nil {
				opts.OnEvent(evt)
			}
			eventBridge.Publish(evt)
		},
		StepLifecycle:         opts.StepLifecycle,
		LifecycleTaskFinished: opts.LifecycleTaskFinished,
		LifecycleRuntimeAbort: opts.LifecycleRuntimeAbort,
		DurabilityObserver:    opts.DurabilityObserver,
		ApplyWorktreeTarget:   opts.ApplyWorktreeTarget,
	})
	if err != nil {
		return nil, err
	}
	return &RuntimeWiring{
		Engine:      eng,
		AskBroker:   askBroker,
		EventBridge: eventBridge,
		Background:  background,
		LocalTools:  localTools,
	}, nil
}

type launchPromptFacingSnapshotReloader struct {
	store                               *session.Store
	workspaceRoot                       string
	configRoot                          string
	skipContinuationAgentRoleValidation bool
}

func (r launchPromptFacingSnapshotReloader) ReloadPromptFacingSnapshotConfig(context.Context, string) (runtime.PromptFacingSnapshotConfig, error) {
	app, err := config.Load(r.workspaceRoot, config.LoadOptions{ConfigRoot: r.configRoot})
	if err != nil {
		return runtime.PromptFacingSnapshotConfig{}, err
	}
	resolved, err := launch.ResolvePromptFacingSnapshotConfig(app, r.store, r.skipContinuationAgentRoleValidation)
	if err != nil {
		return runtime.PromptFacingSnapshotConfig{}, err
	}
	return runtime.PromptFacingSnapshotConfig{
		Settings:      resolved.Settings,
		Source:        resolved.Source,
		ActiveToolIDs: append([]toolspec.ID(nil), resolved.ActiveToolIDs...),
		WebSearchMode: resolved.WebSearchMode,
	}, nil
}

type providerRuntimeSettings struct {
	Model                        string
	ProviderOverride             string
	OpenAIBaseURL                string
	ModelVerbosity               config.ModelVerbosity
	ProviderIdentifier           string
	Store                        bool
	ContextWindowTokens          int
	Auth                         string
	ProviderCapabilitiesOverride *llm.ProviderCapabilities
}

func mainProviderRuntimeSettings(active config.Settings) providerRuntimeSettings {
	return providerRuntimeSettings{
		Model:                        active.Model,
		ProviderOverride:             active.ProviderOverride,
		OpenAIBaseURL:                active.OpenAIBaseURL,
		ModelVerbosity:               active.ModelVerbosity,
		ProviderIdentifier:           active.ProviderIdentifier,
		Store:                        active.Store,
		ContextWindowTokens:          active.ModelContextWindow,
		Auth:                         "inherit",
		ProviderCapabilitiesOverride: providerCapabilitiesOverridePtr(active.ProviderCapabilities),
	}
}

func lockedModelCapabilitiesForConfig(model string, override config.ModelCapabilitiesOverride, sources map[string]string, reasoningKey string, visionKey string) session.LockedModelCapabilities {
	locked := llm.LockedModelCapabilitiesForModel(model)
	reasoningConfigured := inheritedModelCapabilitySourceConfigured(sources, reasoningKey)
	visionConfigured := inheritedModelCapabilitySourceConfigured(sources, visionKey)
	if reasoningConfigured {
		locked.SupportsReasoningEffort = override.SupportsReasoningEffort
	}
	if visionConfigured {
		locked.SupportsVisionInputs = override.SupportsVisionInputs
	}
	if reasoningConfigured || visionConfigured {
		return locked
	}
	return llm.LockedModelCapabilitiesForConfig(model, override)
}

func inheritedModelCapabilitySourceConfigured(sources map[string]string, key string) bool {
	if modelCapabilitySourceConfigured(sources, key) {
		return true
	}
	switch key {
	case "reviewer.model_capabilities.supports_reasoning_effort":
		return modelCapabilitySourceConfigured(sources, "model_capabilities.supports_reasoning_effort")
	case "reviewer.model_capabilities.supports_vision_inputs":
		return modelCapabilitySourceConfigured(sources, "model_capabilities.supports_vision_inputs")
	default:
		return false
	}
}

func modelCapabilitySourceConfigured(sources map[string]string, key string) bool {
	switch strings.TrimSpace(sources[key]) {
	case "file", "env", "cli", "subagent":
		return true
	default:
		return false
	}
}

func reviewerProviderRuntimeSettings(active config.Settings) providerRuntimeSettings {
	reviewer := active.Reviewer
	reviewerProvider := config.ResolveReviewerProviderSettings(config.Settings{
		ProviderOverride: active.ProviderOverride,
		OpenAIBaseURL:    active.OpenAIBaseURL,
		Reviewer:         reviewer,
	})
	return providerRuntimeSettings{
		Model:                        reviewer.Model,
		ProviderOverride:             reviewerProvider.ProviderOverride,
		OpenAIBaseURL:                reviewerProvider.OpenAIBaseURL,
		ModelVerbosity:               reviewer.ModelVerbosity,
		ProviderIdentifier:           active.ProviderIdentifier,
		Store:                        false,
		ContextWindowTokens:          reviewer.ModelContextWindow,
		Auth:                         reviewer.Auth,
		ProviderCapabilitiesOverride: providerCapabilitiesOverridePtr(reviewer.ProviderCapabilities),
	}
}

func providerCapabilitiesOverridePtr(override config.ProviderCapabilitiesOverride) *llm.ProviderCapabilities {
	caps, ok := llm.ProviderCapabilitiesFromOverride(override)
	if !ok {
		return nil
	}
	return &caps
}

func boolRef(v bool) *bool { return &v }
