package protoapi

import (
	"fmt"
	"sort"

	"core/shared/config"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/toolspec"
)

func sessionSettingsToProto(settings config.Settings) (*sessionlaunchpb.Settings, error) {
	modelVerbosity, err := modelVerbosityToProto(settings.ModelVerbosity)
	if err != nil {
		return nil, err
	}
	compactionMode, err := compactionModeToProto(settings.CompactionMode)
	if err != nil {
		return nil, err
	}
	backgroundOutput, err := backgroundShellOutputToProto(settings.BGShellsOutput)
	if err != nil {
		return nil, err
	}
	shellMode, err := shellPostprocessingModeToProto(settings.Shell.PostprocessingMode)
	if err != nil {
		return nil, err
	}
	cacheWarningMode, err := cacheWarningModeToProto(settings.CacheWarningMode)
	if err != nil {
		return nil, err
	}
	workflowMode, err := workflowCompletionModeToProto(settings.Workflow.CompletionMode)
	if err != nil {
		return nil, err
	}
	sleepMode, err := sleepPreventionModeToProto(settings.PreventSleep)
	if err != nil {
		return nil, err
	}
	systemPromptFiles, err := systemPromptFilesToProto(settings.SystemPromptFiles)
	if err != nil {
		return nil, err
	}
	enabledTools, err := enabledToolFactsToProto(settings.EnabledTools)
	if err != nil {
		return nil, err
	}
	subagents, err := subagentRolesToProto(settings)
	if err != nil {
		return nil, err
	}
	serverPort, err := projectInt32(settings.ServerPort, "server port")
	if err != nil {
		return nil, err
	}
	modelContextWindow, err := projectInt32(settings.ModelContextWindow, "model context window")
	if err != nil {
		return nil, err
	}
	compactionThreshold, err := projectInt32(settings.ContextCompactionThresholdTokens, "context compaction threshold")
	if err != nil {
		return nil, err
	}
	preSubmitLead, err := projectInt32(settings.PreSubmitCompactionLeadTokens, "pre-submit compaction lead")
	if err != nil {
		return nil, err
	}
	minimumExec, err := projectInt32(settings.MinimumExecToBgSeconds, "minimum exec-to-background seconds")
	if err != nil {
		return nil, err
	}
	modelTimeout, err := projectInt32(settings.Timeouts.ModelRequestSeconds, "model request timeout")
	if err != nil {
		return nil, err
	}
	shellOutputMax, err := projectInt32(settings.ShellOutputMaxChars, "shell output maximum")
	if err != nil {
		return nil, err
	}
	worktreeTimeout, err := projectInt32(settings.Worktrees.SetupTimeoutSeconds, "worktree setup timeout")
	if err != nil {
		return nil, err
	}
	workflowConcurrency, err := projectInt32(settings.Workflow.Concurrency, "workflow concurrency")
	if err != nil {
		return nil, err
	}
	workflowAttempts, err := projectInt32(settings.Workflow.MaxInvalidCompletionAttempts, "workflow invalid completion attempts")
	if err != nil {
		return nil, err
	}
	maxSubagentDepth, err := projectInt32(settings.MaxSubagentDepth, "maximum subagent depth")
	if err != nil {
		return nil, err
	}
	reviewer, err := reviewerSettingsToProto(settings.Reviewer)
	if err != nil {
		return nil, err
	}
	message := &sessionlaunchpb.Settings{
		Model:                            settings.Model,
		ThinkingLevel:                    settings.ThinkingLevel,
		ModelVerbosity:                   modelVerbosity,
		SystemPromptFile:                 settings.SystemPromptFile,
		SystemPromptFiles:                systemPromptFiles,
		ModelCapabilities:                modelCapabilitiesToProto(settings.ModelCapabilities),
		Theme:                            settings.Theme,
		NotificationMethod:               settings.NotificationMethod,
		ToolPreambles:                    settings.ToolPreambles,
		PriorityRequestMode:              settings.PriorityRequestMode,
		Debug:                            settings.Debug,
		ServerHost:                       settings.ServerHost,
		ServerPort:                       serverPort,
		WebSearch:                        settings.WebSearch,
		ProviderOverride:                 settings.ProviderOverride,
		ProviderIdentifier:               settings.ProviderIdentifier,
		OpenaiBaseUrl:                    settings.OpenAIBaseURL,
		ProviderCapabilities:             providerCapabilitiesToProto(settings.ProviderCapabilities),
		Store:                            settings.Store,
		AllowNonCwdEdits:                 settings.AllowNonCwdEdits,
		ModelContextWindow:               modelContextWindow,
		ContextCompactionThresholdTokens: compactionThreshold,
		PreSubmitCompactionLeadTokens:    preSubmitLead,
		MinimumExecToBgSeconds:           minimumExec,
		CompactionMode:                   compactionMode,
		EnabledTools:                     enabledTools,
		SkillToggles:                     booleanFactsToProto(settings.SkillToggles),
		Timeouts:                         &sessionlaunchpb.TimeoutSettings{ModelRequestSeconds: modelTimeout},
		ShellOutputMaxChars:              shellOutputMax,
		BgShellsOutput:                   backgroundOutput,
		Shell: &sessionlaunchpb.ShellSettings{
			PostprocessingMode: shellMode,
			PostprocessHook:    clonePointer(settings.Shell.PostprocessHook),
		},
		CacheWarningMode: cacheWarningMode,
		Worktrees: &sessionlaunchpb.WorktreeSettings{
			BaseDir: settings.Worktrees.BaseDir, SetupScript: settings.Worktrees.SetupScript,
			SetupTimeoutSeconds: worktreeTimeout,
		},
		Workflow: &sessionlaunchpb.WorkflowSettings{
			CompletionMode: workflowMode, Concurrency: workflowConcurrency,
			MaxInvalidCompletionAttempts: workflowAttempts,
			UseRequiredToolCalls:         settings.Workflow.UseRequiredToolCalls,
			Subagents:                    settings.Workflow.Subagents,
		},
		Reviewer:         reviewer,
		Subagents:        subagents,
		MaxSubagentDepth: maxSubagentDepth,
		PreventSleep:     sleepMode,
	}
	if settings.Workflow.PreCompactionTokens != nil {
		value, conversionErr := projectInt32(*settings.Workflow.PreCompactionTokens, "workflow pre-compaction tokens")
		if conversionErr != nil {
			return nil, conversionErr
		}
		message.Workflow.PreCompactionTokens = &value
	}
	return message, Validate(message)
}

func sessionSettingsFromProto(message *sessionlaunchpb.Settings) (config.Settings, error) {
	if err := Validate(message); err != nil {
		return config.Settings{}, err
	}
	modelVerbosity, err := modelVerbosityFromProto(message.ModelVerbosity)
	if err != nil {
		return config.Settings{}, err
	}
	compactionMode, err := compactionModeFromProto(message.CompactionMode)
	if err != nil {
		return config.Settings{}, err
	}
	backgroundOutput, err := backgroundShellOutputFromProto(message.BgShellsOutput)
	if err != nil {
		return config.Settings{}, err
	}
	shellMode, err := shellPostprocessingModeFromProto(message.Shell.PostprocessingMode)
	if err != nil {
		return config.Settings{}, err
	}
	cacheWarningMode, err := cacheWarningModeFromProto(message.CacheWarningMode)
	if err != nil {
		return config.Settings{}, err
	}
	workflowMode, err := workflowCompletionModeFromProto(message.Workflow.CompletionMode)
	if err != nil {
		return config.Settings{}, err
	}
	sleepMode, err := sleepPreventionModeFromProto(message.PreventSleep)
	if err != nil {
		return config.Settings{}, err
	}
	systemPromptFiles, err := systemPromptFilesFromProto(message.SystemPromptFiles)
	if err != nil {
		return config.Settings{}, err
	}
	enabledTools, err := enabledToolFactsFromProto(message.EnabledTools)
	if err != nil {
		return config.Settings{}, err
	}
	skillToggles, err := booleanFactsFromProto(message.SkillToggles)
	if err != nil {
		return config.Settings{}, err
	}
	subagents, err := subagentRolesFromProto(message.Subagents)
	if err != nil {
		return config.Settings{}, err
	}
	reviewer, err := reviewerSettingsFromProto(message.Reviewer)
	if err != nil {
		return config.Settings{}, err
	}
	settings := config.Settings{
		Model:                            message.Model,
		ThinkingLevel:                    message.ThinkingLevel,
		ModelVerbosity:                   modelVerbosity,
		SystemPromptFile:                 message.SystemPromptFile,
		SystemPromptFiles:                systemPromptFiles,
		ModelCapabilities:                modelCapabilitiesFromProto(message.ModelCapabilities),
		Theme:                            message.Theme,
		NotificationMethod:               message.NotificationMethod,
		ToolPreambles:                    message.ToolPreambles,
		PriorityRequestMode:              message.PriorityRequestMode,
		Debug:                            message.Debug,
		ServerHost:                       message.ServerHost,
		ServerPort:                       int(message.ServerPort),
		WebSearch:                        message.WebSearch,
		ProviderOverride:                 message.ProviderOverride,
		ProviderIdentifier:               message.ProviderIdentifier,
		OpenAIBaseURL:                    message.OpenaiBaseUrl,
		ProviderCapabilities:             providerCapabilitiesFromProto(message.ProviderCapabilities),
		Store:                            message.Store,
		AllowNonCwdEdits:                 message.AllowNonCwdEdits,
		ModelContextWindow:               int(message.ModelContextWindow),
		ContextCompactionThresholdTokens: int(message.ContextCompactionThresholdTokens),
		PreSubmitCompactionLeadTokens:    int(message.PreSubmitCompactionLeadTokens),
		MinimumExecToBgSeconds:           int(message.MinimumExecToBgSeconds),
		CompactionMode:                   compactionMode,
		EnabledTools:                     enabledTools,
		SkillToggles:                     skillToggles,
		Timeouts:                         config.Timeouts{ModelRequestSeconds: int(message.Timeouts.ModelRequestSeconds)},
		ShellOutputMaxChars:              int(message.ShellOutputMaxChars),
		BGShellsOutput:                   backgroundOutput,
		Shell: config.ShellSettings{
			PostprocessingMode: shellMode,
			PostprocessHook:    clonePointer(message.Shell.PostprocessHook),
		},
		CacheWarningMode: cacheWarningMode,
		Worktrees: config.WorktreeSettings{
			BaseDir: message.Worktrees.BaseDir, SetupScript: message.Worktrees.SetupScript,
			SetupTimeoutSeconds: int(message.Worktrees.SetupTimeoutSeconds),
		},
		Workflow: config.WorkflowSettings{
			CompletionMode: workflowMode, Concurrency: int(message.Workflow.Concurrency),
			MaxInvalidCompletionAttempts: int(message.Workflow.MaxInvalidCompletionAttempts),
			UseRequiredToolCalls:         message.Workflow.UseRequiredToolCalls,
			Subagents:                    message.Workflow.Subagents,
		},
		Reviewer:         reviewer,
		Subagents:        subagents,
		MaxSubagentDepth: int(message.MaxSubagentDepth),
		PreventSleep:     sleepMode,
	}
	if message.Workflow.PreCompactionTokens != nil {
		value := int(*message.Workflow.PreCompactionTokens)
		settings.Workflow.PreCompactionTokens = &value
	}
	return settings, nil
}

func reviewerSettingsToProto(settings config.ReviewerSettings) (*sessionlaunchpb.ReviewerSettings, error) {
	verbosity, err := modelVerbosityToProto(settings.ModelVerbosity)
	if err != nil {
		return nil, err
	}
	window, err := projectInt32(settings.ModelContextWindow, "reviewer model context window")
	if err != nil {
		return nil, err
	}
	timeout, err := projectInt32(settings.TimeoutSeconds, "reviewer timeout")
	if err != nil {
		return nil, err
	}
	return &sessionlaunchpb.ReviewerSettings{
		Frequency: settings.Frequency, Model: settings.Model, ThinkingLevel: settings.ThinkingLevel,
		ModelVerbosity: verbosity, ProviderOverride: settings.ProviderOverride,
		OpenaiBaseUrl: settings.OpenAIBaseURL, ModelCapabilities: modelCapabilitiesToProto(settings.ModelCapabilities),
		ProviderCapabilities: providerCapabilitiesToProto(settings.ProviderCapabilities),
		ModelContextWindow:   window, Auth: settings.Auth, SystemPromptFile: settings.SystemPromptFile,
		TimeoutSeconds: timeout, VerboseOutput: settings.VerboseOutput,
	}, nil
}

func reviewerSettingsFromProto(message *sessionlaunchpb.ReviewerSettings) (config.ReviewerSettings, error) {
	verbosity, err := modelVerbosityFromProto(message.ModelVerbosity)
	if err != nil {
		return config.ReviewerSettings{}, err
	}
	return config.ReviewerSettings{
		Frequency: message.Frequency, Model: message.Model, ThinkingLevel: message.ThinkingLevel,
		ModelVerbosity: verbosity, ProviderOverride: message.ProviderOverride,
		OpenAIBaseURL: message.OpenaiBaseUrl, ModelCapabilities: modelCapabilitiesFromProto(message.ModelCapabilities),
		ProviderCapabilities: providerCapabilitiesFromProto(message.ProviderCapabilities),
		ModelContextWindow:   int(message.ModelContextWindow), Auth: message.Auth,
		SystemPromptFile: message.SystemPromptFile, TimeoutSeconds: int(message.TimeoutSeconds),
		VerboseOutput: message.VerboseOutput,
	}, nil
}

func modelCapabilitiesToProto(value config.ModelCapabilitiesOverride) *sessionlaunchpb.ModelCapabilitiesOverride {
	return &sessionlaunchpb.ModelCapabilitiesOverride{
		SupportsReasoningEffort: value.SupportsReasoningEffort,
		SupportsVisionInputs:    value.SupportsVisionInputs,
	}
}

func modelCapabilitiesFromProto(value *sessionlaunchpb.ModelCapabilitiesOverride) config.ModelCapabilitiesOverride {
	return config.ModelCapabilitiesOverride{
		SupportsReasoningEffort: value.SupportsReasoningEffort,
		SupportsVisionInputs:    value.SupportsVisionInputs,
	}
}

func providerCapabilitiesToProto(value config.ProviderCapabilitiesOverride) *sessionlaunchpb.ProviderCapabilitiesOverride {
	return &sessionlaunchpb.ProviderCapabilitiesOverride{
		ProviderId: value.ProviderID, SupportsResponsesApi: value.SupportsResponsesAPI,
		SupportsResponsesCompact: value.SupportsResponsesCompact,
		SupportsPromptCacheKey:   value.SupportsPromptCacheKey, SupportsNativeWebSearch: value.SupportsNativeWebSearch,
		SupportsReasoningEncrypted:    value.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit: value.SupportsServerSideContextEdit,
		SupportsProviderVerbosity:     value.SupportsProviderVerbosity, IsOpenaiFirstParty: value.IsOpenAIFirstParty,
	}
}

func providerCapabilitiesFromProto(value *sessionlaunchpb.ProviderCapabilitiesOverride) config.ProviderCapabilitiesOverride {
	return config.ProviderCapabilitiesOverride{
		ProviderID: value.ProviderId, SupportsResponsesAPI: value.SupportsResponsesApi,
		SupportsResponsesCompact: value.SupportsResponsesCompact,
		SupportsPromptCacheKey:   value.SupportsPromptCacheKey, SupportsNativeWebSearch: value.SupportsNativeWebSearch,
		SupportsReasoningEncrypted:    value.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit: value.SupportsServerSideContextEdit,
		SupportsProviderVerbosity:     value.SupportsProviderVerbosity, IsOpenAIFirstParty: value.IsOpenaiFirstParty,
	}
}

func systemPromptFilesToProto(values []config.SystemPromptFile) ([]*sessionlaunchpb.SystemPromptFile, error) {
	result := make([]*sessionlaunchpb.SystemPromptFile, 0, len(values))
	for _, value := range values {
		scope, err := systemPromptFileScopeToProto(value.Scope)
		if err != nil {
			return nil, err
		}
		result = append(result, &sessionlaunchpb.SystemPromptFile{Path: value.Path, Scope: scope})
	}
	return result, nil
}

func systemPromptFilesFromProto(values []*sessionlaunchpb.SystemPromptFile) ([]config.SystemPromptFile, error) {
	result := make([]config.SystemPromptFile, 0, len(values))
	for _, value := range values {
		scope, err := systemPromptFileScopeFromProto(value.Scope)
		if err != nil {
			return nil, err
		}
		result = append(result, config.SystemPromptFile{Path: value.Path, Scope: scope})
	}
	return result, nil
}

func enabledToolFactsToProto(values map[toolspec.ID]bool) ([]*sessionlaunchpb.ToolEnabledFact, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	result := make([]*sessionlaunchpb.ToolEnabledFact, 0, len(keys))
	for _, key := range keys {
		toolID, err := sessionToolIDToProto(toolspec.ID(key))
		if err != nil {
			return nil, err
		}
		result = append(result, &sessionlaunchpb.ToolEnabledFact{ToolId: toolID, Enabled: values[toolspec.ID(key)]})
	}
	return result, nil
}

func enabledToolFactsFromProto(values []*sessionlaunchpb.ToolEnabledFact) (map[toolspec.ID]bool, error) {
	result := make(map[toolspec.ID]bool, len(values))
	for _, value := range values {
		toolID, err := sessionToolIDFromProto(value.ToolId)
		if err != nil {
			return nil, err
		}
		if _, exists := result[toolID]; exists {
			return nil, fmt.Errorf("duplicate enabled tool fact %q", toolID)
		}
		result[toolID] = value.Enabled
	}
	return result, nil
}

func booleanFactsToProto(values map[string]bool) []*sessionlaunchpb.BooleanFact {
	keys := sortedStringKeys(values)
	result := make([]*sessionlaunchpb.BooleanFact, 0, len(keys))
	for _, key := range keys {
		result = append(result, &sessionlaunchpb.BooleanFact{Key: key, Value: values[key]})
	}
	return result
}

func booleanFactsFromProto(values []*sessionlaunchpb.BooleanFact) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if _, exists := result[value.Key]; exists {
			return nil, fmt.Errorf("duplicate boolean fact %q", value.Key)
		}
		result[value.Key] = value.Value
	}
	return result, nil
}

func stringFactsToProto(values map[string]string) []*sessionlaunchpb.StringFact {
	keys := sortedStringKeys(values)
	result := make([]*sessionlaunchpb.StringFact, 0, len(keys))
	for _, key := range keys {
		result = append(result, &sessionlaunchpb.StringFact{Key: key, Value: values[key]})
	}
	return result
}

func stringFactsFromProto(values []*sessionlaunchpb.StringFact) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		if _, exists := result[value.Key]; exists {
			return nil, fmt.Errorf("duplicate string fact %q", value.Key)
		}
		result[value.Key] = value.Value
	}
	return result, nil
}

func subagentRolesToProto(base config.Settings) ([]*sessionlaunchpb.NamedSubagentRole, error) {
	values := base.Subagents
	keys := sortedStringKeys(values)
	result := make([]*sessionlaunchpb.NamedSubagentRole, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		effective := config.OverlaySubagentRoleSettings(base, value, true)
		effective.Subagents = nil
		settings, err := sessionSettingsToProto(effective)
		if err != nil {
			return nil, fmt.Errorf("subagent %q settings: %w", key, err)
		}
		result = append(result, &sessionlaunchpb.NamedSubagentRole{
			Name: key,
			Role: &sessionlaunchpb.SubagentRole{
				Settings: settings, Sources: stringFactsToProto(value.Sources), Description: value.Description,
				AgentCallable: value.AgentCallable, AgentCallableSet: value.AgentCallableSet,
				WorkflowSubagent: value.WorkflowSubagent, WorkflowSubagentSet: value.WorkflowSubagentSet,
			},
		})
	}
	return result, nil
}

func subagentRolesFromProto(values []*sessionlaunchpb.NamedSubagentRole) (map[string]config.SubagentRole, error) {
	result := make(map[string]config.SubagentRole, len(values))
	for _, value := range values {
		if _, exists := result[value.Name]; exists {
			return nil, fmt.Errorf("duplicate subagent role %q", value.Name)
		}
		settings, err := sessionSettingsFromProto(value.Role.Settings)
		if err != nil {
			return nil, fmt.Errorf("subagent %q settings: %w", value.Name, err)
		}
		sources, err := stringFactsFromProto(value.Role.Sources)
		if err != nil {
			return nil, fmt.Errorf("subagent %q sources: %w", value.Name, err)
		}
		result[value.Name] = config.SubagentRole{
			Settings: settings, Sources: sources, Description: value.Role.Description,
			AgentCallable: value.Role.AgentCallable, AgentCallableSet: value.Role.AgentCallableSet,
			WorkflowSubagent: value.Role.WorkflowSubagent, WorkflowSubagentSet: value.Role.WorkflowSubagentSet,
		}
	}
	return result, nil
}

func sessionSourceReportToProto(source config.SourceReport) (*sessionlaunchpb.SourceReport, error) {
	message := &sessionlaunchpb.SourceReport{
		SettingsPath: source.SettingsPath, SettingsFileExists: source.SettingsFileExists,
		CreatedDefaultConfig: source.CreatedDefaultConfig, HomeSettingsPath: source.HomeSettingsPath,
		HomeSettingsFileExists: source.HomeSettingsFileExists, WorkspaceSettingsPath: source.WorkspaceSettingsPath,
		WorkspaceSettingsFileExists:   source.WorkspaceSettingsFileExists,
		WorkspaceSettingsLayerEnabled: source.WorkspaceSettingsLayerEnabled,
		Sources:                       stringFactsToProto(source.Sources),
	}
	return message, Validate(message)
}

func sessionSourceReportFromProto(message *sessionlaunchpb.SourceReport) (config.SourceReport, error) {
	if err := Validate(message); err != nil {
		return config.SourceReport{}, err
	}
	sources, err := stringFactsFromProto(message.Sources)
	if err != nil {
		return config.SourceReport{}, err
	}
	return config.SourceReport{
		SettingsPath: message.SettingsPath, SettingsFileExists: message.SettingsFileExists,
		CreatedDefaultConfig: message.CreatedDefaultConfig, HomeSettingsPath: message.HomeSettingsPath,
		HomeSettingsFileExists: message.HomeSettingsFileExists, WorkspaceSettingsPath: message.WorkspaceSettingsPath,
		WorkspaceSettingsFileExists:   message.WorkspaceSettingsFileExists,
		WorkspaceSettingsLayerEnabled: message.WorkspaceSettingsLayerEnabled, Sources: sources,
	}, nil
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
