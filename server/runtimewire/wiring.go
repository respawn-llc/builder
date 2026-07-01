package runtimewire

import (
	"context"
	"strings"
	"time"

	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	askquestion "core/server/tools"
	triggerhandofftool "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflowruntime"
	"core/shared/config"
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
	OnEvent                             func(evt runtime.Event)
	Headless                            bool
	FastMode                            *runtime.FastModeState
	Sources                             map[string]string
	Client                              llm.Client
	WorkflowRun                         *workflowruntime.Config
	PromptFacingSnapshotReloader        runtime.PromptFacingSnapshotReloader
	SkipContinuationAgentRoleValidation bool
	// GlobalConfigDir is the absolute persistence root that owns model-visible
	// global context (AGENTS.md, system prompt, skills). Empty falls back to
	// ~/.kent inside the runtime resolvers.
	GlobalConfigDir string
}

func NewRuntimeWiring(store *session.Store, active config.Settings, enabledTools []toolspec.ID, workspaceRoot string, mgr *auth.Manager, logger Logger, opts RuntimeWiringOptions) (*RuntimeWiring, error) {
	return NewRuntimeWiringWithBackground(store, active, enabledTools, workspaceRoot, mgr, logger, nil, opts)
}

func NewRuntimeWiringWithBackground(store *session.Store, active config.Settings, enabledTools []toolspec.ID, workspaceRoot string, mgr *auth.Manager, logger Logger, background *shelltool.Manager, opts RuntimeWiringOptions) (*RuntimeWiring, error) {
	var eng *runtime.Engine
	localTools, askBroker, background, err := NewLocalToolRegistryBinding(
		workspaceRoot,
		store.Meta().SessionID,
		enabledTools,
		time.Duration(active.MinimumExecToBgSeconds)*time.Second,
		active.ShellOutputMaxChars,
		active.AllowNonCwdEdits,
		llm.LockedContractSupportsVisionInputs(store.Meta().Locked, active.Model),
		logger,
		background,
		func() triggerhandofftool.TriggerHandoffController { return eng },
		func() bool {
			if eng == nil {
				return true
			}
			return eng.QuestionsEnabled()
		},
	)
	if err != nil {
		return nil, err
	}
	toolRegistry := localTools.Registry()

	mainProvider := mainProviderRuntimeSettings(active)
	var client llm.Client
	if opts.Client != nil {
		client = opts.Client
	} else {
		var mainAuth llm.AuthHeaderProvider
		if mgr != nil && !strings.EqualFold(strings.TrimSpace(mainProvider.Auth), "none") {
			mainAuth = mgr
		}
		client, err = llm.NewProviderClient(llm.ProviderClientOptions{
			Provider:                     llm.Provider(strings.TrimSpace(mainProvider.ProviderOverride)),
			Model:                        mainProvider.Model,
			Auth:                         mainAuth,
			HTTPClient:                   llm.NewHTTPClient(time.Duration(active.Timeouts.ModelRequestSeconds) * time.Second),
			OpenAIBaseURL:                mainProvider.OpenAIBaseURL,
			ModelVerbosity:               string(mainProvider.ModelVerbosity),
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
		var reviewerAuth llm.AuthHeaderProvider
		if mgr != nil && !strings.EqualFold(strings.TrimSpace(reviewerProvider.Auth), "none") {
			reviewerAuth = mgr
		}
		return llm.NewProviderClient(llm.ProviderClientOptions{
			Provider:                     llm.Provider(strings.TrimSpace(reviewerProvider.ProviderOverride)),
			Model:                        reviewerProvider.Model,
			Auth:                         reviewerAuth,
			HTTPClient:                   llm.NewHTTPClient(time.Duration(active.Reviewer.TimeoutSeconds) * time.Second),
			OpenAIBaseURL:                reviewerProvider.OpenAIBaseURL,
			ModelVerbosity:               string(reviewerProvider.ModelVerbosity),
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
			logger.Logf("runtime.event.drop count=%d kind=%s step_id=%s", total, evt.Kind, evt.StepID)
		}
	})
	promptReloader := opts.PromptFacingSnapshotReloader
	if promptReloader == nil {
		promptReloader = launchPromptFacingSnapshotReloader{
			store:                               store,
			workspaceRoot:                       workspaceRoot,
			configRoot:                          opts.GlobalConfigDir,
			skipContinuationAgentRoleValidation: opts.SkipContinuationAgentRoleValidation,
		}
	}
	eng, err = runtime.New(store, client, toolRegistry, runtime.Config{
		Model:                         active.Model,
		Temperature:                   1,
		MaxTokens:                     0,
		ThinkingLevel:                 active.ThinkingLevel,
		ModelCapabilities:             llm.LockedModelCapabilitiesForConfig(active.Model, active.ModelCapabilities),
		FastModeEnabled:               active.PriorityRequestMode,
		FastModeState:                 opts.FastMode,
		WebSearchMode:                 active.WebSearch,
		PromptFacingSnapshotReloader:  promptReloader,
		ProviderCapabilitiesOverride:  mainProvider.ProviderCapabilitiesOverride,
		EnabledTools:                  enabledTools,
		DisabledSkills:                config.DisabledSkillToggles(active),
		SubagentCatalogSettings:       active,
		SystemPromptFiles:             active.SystemPromptFiles,
		AutoCompactTokenLimit:         active.ContextCompactionThresholdTokens,
		PreSubmitCompactionLeadTokens: active.PreSubmitCompactionLeadTokens,
		ContextWindowTokens:           active.ModelContextWindow,
		EffectiveContextWindowPercent: 95,
		LocalCompactionCarryoverLimit: 20_000,
		CompactionMode:                string(active.CompactionMode),
		CacheWarningMode:              active.CacheWarningMode,
		AutoCompactionEnabled:         boolRef(true),
		QuestionsEnabled:              boolRef(true),
		HeadlessMode:                  opts.Headless,
		ToolPreambles:                 active.ToolPreambles,
		WorkflowRun:                   opts.WorkflowRun,
		TranscriptWorkingDir:          workspaceRoot,
		GlobalConfigDir:               opts.GlobalConfigDir,
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
