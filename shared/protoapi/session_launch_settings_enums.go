package protoapi

import (
	"fmt"

	"core/shared/config"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
)

func modelVerbosityToProto(value config.ModelVerbosity) (sessionlaunchpb.ModelVerbosity, error) {
	switch value {
	case "":
		return sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_UNSPECIFIED, nil
	case config.ModelVerbosityLow:
		return sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_LOW, nil
	case config.ModelVerbosityMedium:
		return sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_MEDIUM, nil
	case config.ModelVerbosityHigh:
		return sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_HIGH, nil
	default:
		return 0, fmt.Errorf("model verbosity %q is unsupported", value)
	}
}

func modelVerbosityFromProto(value sessionlaunchpb.ModelVerbosity) (config.ModelVerbosity, error) {
	switch value {
	case sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_LOW:
		return config.ModelVerbosityLow, nil
	case sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_MEDIUM:
		return config.ModelVerbosityMedium, nil
	case sessionlaunchpb.ModelVerbosity_MODEL_VERBOSITY_HIGH:
		return config.ModelVerbosityHigh, nil
	default:
		return "", fmt.Errorf("protobuf model verbosity %v is unsupported", value)
	}
}

func compactionModeToProto(value config.CompactionMode) (sessionlaunchpb.CompactionMode, error) {
	switch value {
	case "":
		return sessionlaunchpb.CompactionMode_COMPACTION_MODE_UNSPECIFIED, nil
	case config.CompactionModeNative:
		return sessionlaunchpb.CompactionMode_COMPACTION_MODE_NATIVE, nil
	case config.CompactionModeLocal:
		return sessionlaunchpb.CompactionMode_COMPACTION_MODE_LOCAL, nil
	case config.CompactionModeNone:
		return sessionlaunchpb.CompactionMode_COMPACTION_MODE_NONE, nil
	default:
		return 0, fmt.Errorf("compaction mode %q is unsupported", value)
	}
}

func compactionModeFromProto(value sessionlaunchpb.CompactionMode) (config.CompactionMode, error) {
	switch value {
	case sessionlaunchpb.CompactionMode_COMPACTION_MODE_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.CompactionMode_COMPACTION_MODE_NATIVE:
		return config.CompactionModeNative, nil
	case sessionlaunchpb.CompactionMode_COMPACTION_MODE_LOCAL:
		return config.CompactionModeLocal, nil
	case sessionlaunchpb.CompactionMode_COMPACTION_MODE_NONE:
		return config.CompactionModeNone, nil
	default:
		return "", fmt.Errorf("protobuf compaction mode %v is unsupported", value)
	}
}

func backgroundShellOutputToProto(value config.BGShellsOutputMode) (sessionlaunchpb.BackgroundShellOutputMode, error) {
	switch value {
	case "":
		return sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_UNSPECIFIED, nil
	case config.BGShellsOutputDefault:
		return sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_DEFAULT, nil
	case config.BGShellsOutputVerbose:
		return sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_VERBOSE, nil
	case config.BGShellsOutputConcise:
		return sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_CONCISE, nil
	default:
		return 0, fmt.Errorf("background shell output mode %q is unsupported", value)
	}
}

func backgroundShellOutputFromProto(value sessionlaunchpb.BackgroundShellOutputMode) (config.BGShellsOutputMode, error) {
	switch value {
	case sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_DEFAULT:
		return config.BGShellsOutputDefault, nil
	case sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_VERBOSE:
		return config.BGShellsOutputVerbose, nil
	case sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_CONCISE:
		return config.BGShellsOutputConcise, nil
	default:
		return "", fmt.Errorf("protobuf background shell output mode %v is unsupported", value)
	}
}

func shellPostprocessingModeToProto(value config.ShellPostprocessingMode) (sessionlaunchpb.ShellPostprocessingMode, error) {
	switch value {
	case "":
		return sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_UNSPECIFIED, nil
	case config.ShellPostprocessingModeNone:
		return sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_NONE, nil
	case config.ShellPostprocessingModeBuiltin:
		return sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_BUILTIN, nil
	case config.ShellPostprocessingModeUser:
		return sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_USER, nil
	case config.ShellPostprocessingModeAll:
		return sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_ALL, nil
	default:
		return 0, fmt.Errorf("shell postprocessing mode %q is unsupported", value)
	}
}

func shellPostprocessingModeFromProto(value sessionlaunchpb.ShellPostprocessingMode) (config.ShellPostprocessingMode, error) {
	switch value {
	case sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_NONE:
		return config.ShellPostprocessingModeNone, nil
	case sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_BUILTIN:
		return config.ShellPostprocessingModeBuiltin, nil
	case sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_USER:
		return config.ShellPostprocessingModeUser, nil
	case sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_ALL:
		return config.ShellPostprocessingModeAll, nil
	default:
		return "", fmt.Errorf("protobuf shell postprocessing mode %v is unsupported", value)
	}
}

func cacheWarningModeToProto(value config.CacheWarningMode) (sessionlaunchpb.CacheWarningMode, error) {
	switch value {
	case "":
		return sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_UNSPECIFIED, nil
	case config.CacheWarningModeOff:
		return sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_OFF, nil
	case config.CacheWarningModeDefault:
		return sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_DEFAULT, nil
	case config.CacheWarningModeVerbose:
		return sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_VERBOSE, nil
	default:
		return 0, fmt.Errorf("cache warning mode %q is unsupported", value)
	}
}

func cacheWarningModeFromProto(value sessionlaunchpb.CacheWarningMode) (config.CacheWarningMode, error) {
	switch value {
	case sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_OFF:
		return config.CacheWarningModeOff, nil
	case sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_DEFAULT:
		return config.CacheWarningModeDefault, nil
	case sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_VERBOSE:
		return config.CacheWarningModeVerbose, nil
	default:
		return "", fmt.Errorf("protobuf cache warning mode %v is unsupported", value)
	}
}

func workflowCompletionModeToProto(value config.WorkflowCompletionMode) (sessionlaunchpb.WorkflowCompletionMode, error) {
	switch value {
	case "":
		return sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_UNSPECIFIED, nil
	case config.WorkflowCompletionModeAuto:
		return sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_AUTO, nil
	case config.WorkflowCompletionModeStructuredOutput:
		return sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_STRUCTURED_OUTPUT, nil
	case config.WorkflowCompletionModeTool:
		return sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_TOOL, nil
	case config.WorkflowCompletionModeShellCommand:
		return sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_SHELL_COMMAND, nil
	case config.WorkflowCompletionModeUnstructured:
		return sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_UNSTRUCTURED_OUTPUT, nil
	default:
		return 0, fmt.Errorf("workflow completion mode %q is unsupported", value)
	}
}

func workflowCompletionModeFromProto(value sessionlaunchpb.WorkflowCompletionMode) (config.WorkflowCompletionMode, error) {
	switch value {
	case sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_AUTO:
		return config.WorkflowCompletionModeAuto, nil
	case sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_STRUCTURED_OUTPUT:
		return config.WorkflowCompletionModeStructuredOutput, nil
	case sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_TOOL:
		return config.WorkflowCompletionModeTool, nil
	case sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_SHELL_COMMAND:
		return config.WorkflowCompletionModeShellCommand, nil
	case sessionlaunchpb.WorkflowCompletionMode_WORKFLOW_COMPLETION_MODE_UNSTRUCTURED_OUTPUT:
		return config.WorkflowCompletionModeUnstructured, nil
	default:
		return "", fmt.Errorf("protobuf workflow completion mode %v is unsupported", value)
	}
}

func sleepPreventionModeToProto(value config.SleepPreventionMode) (sessionlaunchpb.SleepPreventionMode, error) {
	switch value {
	case "":
		return sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_UNSPECIFIED, nil
	case config.SleepPreventionModeAlways:
		return sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_ALWAYS, nil
	case config.SleepPreventionModeActive:
		return sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_ACTIVE, nil
	case config.SleepPreventionModeNever:
		return sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_NEVER, nil
	default:
		return 0, fmt.Errorf("sleep prevention mode %q is unsupported", value)
	}
}

func sleepPreventionModeFromProto(value sessionlaunchpb.SleepPreventionMode) (config.SleepPreventionMode, error) {
	switch value {
	case sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_UNSPECIFIED:
		return "", nil
	case sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_ALWAYS:
		return config.SleepPreventionModeAlways, nil
	case sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_ACTIVE:
		return config.SleepPreventionModeActive, nil
	case sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_NEVER:
		return config.SleepPreventionModeNever, nil
	default:
		return "", fmt.Errorf("protobuf sleep prevention mode %v is unsupported", value)
	}
}

func systemPromptFileScopeToProto(value config.SystemPromptFileScope) (sessionlaunchpb.SystemPromptFileScope, error) {
	switch value {
	case config.SystemPromptFileScopeHomeConfig:
		return sessionlaunchpb.SystemPromptFileScope_SYSTEM_PROMPT_FILE_SCOPE_HOME_CONFIG, nil
	case config.SystemPromptFileScopeWorkspaceConfig:
		return sessionlaunchpb.SystemPromptFileScope_SYSTEM_PROMPT_FILE_SCOPE_WORKSPACE_CONFIG, nil
	case config.SystemPromptFileScopeSubagent:
		return sessionlaunchpb.SystemPromptFileScope_SYSTEM_PROMPT_FILE_SCOPE_SUBAGENT, nil
	default:
		return 0, fmt.Errorf("system prompt file scope %q is unsupported", value)
	}
}

func systemPromptFileScopeFromProto(value sessionlaunchpb.SystemPromptFileScope) (config.SystemPromptFileScope, error) {
	switch value {
	case sessionlaunchpb.SystemPromptFileScope_SYSTEM_PROMPT_FILE_SCOPE_HOME_CONFIG:
		return config.SystemPromptFileScopeHomeConfig, nil
	case sessionlaunchpb.SystemPromptFileScope_SYSTEM_PROMPT_FILE_SCOPE_WORKSPACE_CONFIG:
		return config.SystemPromptFileScopeWorkspaceConfig, nil
	case sessionlaunchpb.SystemPromptFileScope_SYSTEM_PROMPT_FILE_SCOPE_SUBAGENT:
		return config.SystemPromptFileScopeSubagent, nil
	default:
		return "", fmt.Errorf("protobuf system prompt file scope %v is unsupported", value)
	}
}
