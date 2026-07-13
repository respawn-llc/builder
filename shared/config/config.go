package config

import (
	"core/shared/protocol"
	"core/shared/toolspec"
	"net"
	"path/filepath"
	"strconv"
)

const (
	DefaultAppName       = Command
	DefaultPersistence   = PersistenceRoot
	databaseDirName      = "db"
	globalAuthConfigName = "auth.json"
	// PersistenceRootEnvName is the env var that sets the config+data root,
	// equivalent to the --persistence-root flag (LoadOptions.ConfigRoot).
	PersistenceRootEnvName = "KENT_PERSISTENCE_ROOT"
)

type CompactionMode string
type BGShellsOutputMode string
type CacheWarningMode string
type ModelVerbosity string
type ShellPostprocessingMode string
type WorkflowCompletionMode string
type SleepPreventionMode string

type WorktreeSettings struct {
	BaseDir             string
	SetupScript         string
	SetupTimeoutSeconds int
}

type WorkflowSettings struct {
	CompletionMode               WorkflowCompletionMode
	Concurrency                  int
	MaxInvalidCompletionAttempts int
	Subagents                    bool
}

const (
	CompactionModeNative CompactionMode = "native"
	CompactionModeLocal  CompactionMode = "local"
	CompactionModeNone   CompactionMode = "none"

	BGShellsOutputDefault BGShellsOutputMode = "default"
	BGShellsOutputVerbose BGShellsOutputMode = "verbose"
	BGShellsOutputConcise BGShellsOutputMode = "concise"

	CacheWarningModeOff     CacheWarningMode = "off"
	CacheWarningModeDefault CacheWarningMode = "default"
	CacheWarningModeVerbose CacheWarningMode = "verbose"

	ModelVerbosityLow    ModelVerbosity = "low"
	ModelVerbosityMedium ModelVerbosity = "medium"
	ModelVerbosityHigh   ModelVerbosity = "high"

	ShellPostprocessingModeNone    ShellPostprocessingMode = "none"
	ShellPostprocessingModeBuiltin ShellPostprocessingMode = "builtin"
	ShellPostprocessingModeUser    ShellPostprocessingMode = "user"
	ShellPostprocessingModeAll     ShellPostprocessingMode = "all"

	WorkflowCompletionModeAuto             WorkflowCompletionMode = "auto"
	WorkflowCompletionModeStructuredOutput WorkflowCompletionMode = "structured_output"
	WorkflowCompletionModeTool             WorkflowCompletionMode = "tool"
	WorkflowCompletionModeShellCommand     WorkflowCompletionMode = "shell_command"
	WorkflowCompletionModeUnstructured     WorkflowCompletionMode = "unstructured_output"

	SleepPreventionModeAlways SleepPreventionMode = "always"
	SleepPreventionModeActive SleepPreventionMode = "active"
	SleepPreventionModeNever  SleepPreventionMode = "never"
)

type LoadOptions struct {
	Model               string
	ProviderOverride    string
	ThinkingLevel       string
	Theme               string
	ModelTimeoutSeconds int
	Tools               string
	OpenAIBaseURL       string
	ConfigRoot          string
}

type Timeouts struct {
	ModelRequestSeconds int
}

type ShellSettings struct {
	PostprocessingMode ShellPostprocessingMode
	PostprocessHook    string
}

type SubagentRole struct {
	Settings            Settings
	Sources             map[string]string
	Description         string
	AgentCallable       bool
	AgentCallableSet    bool
	WorkflowSubagent    bool
	WorkflowSubagentSet bool
}

type SystemPromptFileScope string

const (
	SystemPromptFileScopeHomeConfig      SystemPromptFileScope = "home_config"
	SystemPromptFileScopeWorkspaceConfig SystemPromptFileScope = "workspace_config"
	SystemPromptFileScopeSubagent        SystemPromptFileScope = "subagent"
)

type SystemPromptFile struct {
	Path  string
	Scope SystemPromptFileScope
}

type Settings struct {
	Model                            string
	ThinkingLevel                    string
	ModelVerbosity                   ModelVerbosity
	SystemPromptFile                 string
	SystemPromptFiles                []SystemPromptFile
	ModelCapabilities                ModelCapabilitiesOverride
	Theme                            string
	NotificationMethod               string
	ToolPreambles                    bool
	PriorityRequestMode              bool
	Debug                            bool
	ServerHost                       string
	ServerPort                       int
	WebSearch                        string
	ProviderOverride                 string
	ProviderIdentifier               string
	OpenAIBaseURL                    string
	ProviderCapabilities             ProviderCapabilitiesOverride
	Store                            bool
	AllowNonCwdEdits                 bool
	ModelContextWindow               int
	ContextCompactionThresholdTokens int
	PreSubmitCompactionLeadTokens    int
	MinimumExecToBgSeconds           int
	CompactionMode                   CompactionMode
	EnabledTools                     map[toolspec.ID]bool
	SkillToggles                     map[string]bool
	Timeouts                         Timeouts
	ShellOutputMaxChars              int
	BGShellsOutput                   BGShellsOutputMode
	Shell                            ShellSettings
	CacheWarningMode                 CacheWarningMode
	Worktrees                        WorktreeSettings
	Workflow                         WorkflowSettings
	Reviewer                         ReviewerSettings
	Subagents                        map[string]SubagentRole
	PreventSleep                     SleepPreventionMode
}

type ModelCapabilitiesOverride struct {
	SupportsReasoningEffort bool
	SupportsVisionInputs    bool
}

type ProviderCapabilitiesOverride struct {
	ProviderID                     string
	SupportsResponsesAPI           bool
	SupportsResponsesCompact       bool
	SupportsRequestInputTokenCount bool
	SupportsPromptCacheKey         bool
	SupportsNativeWebSearch        bool
	SupportsReasoningEncrypted     bool
	SupportsServerSideContextEdit  bool
	SupportsProviderVerbosity      bool
	IsOpenAIFirstParty             bool
}

type ReviewerSettings struct {
	Frequency            string
	Model                string
	ThinkingLevel        string
	ModelVerbosity       ModelVerbosity
	ProviderOverride     string
	OpenAIBaseURL        string
	ModelCapabilities    ModelCapabilitiesOverride
	ProviderCapabilities ProviderCapabilitiesOverride
	ModelContextWindow   int
	Auth                 string
	SystemPromptFile     string
	TimeoutSeconds       int
	VerboseOutput        bool
}

type ReviewerProviderSettings struct {
	ProviderOverride string
	OpenAIBaseURL    string
}

type SourceReport struct {
	SettingsPath                  string
	SettingsFileExists            bool
	CreatedDefaultConfig          bool
	HomeSettingsPath              string
	HomeSettingsFileExists        bool
	WorkspaceSettingsPath         string
	WorkspaceSettingsFileExists   bool
	WorkspaceSettingsLayerEnabled bool
	Sources                       map[string]string
}

type App struct {
	AppName         string
	WorkspaceRoot   string
	PersistenceRoot string
	Settings        Settings
	Source          SourceReport
}

type settingsFile map[string]any

func EnabledToolIDs(v Settings) []toolspec.ID {
	ids := make([]toolspec.ID, 0, len(v.EnabledTools))
	for _, id := range toolspec.CatalogIDs() {
		if v.EnabledTools[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func ProjectSessionDir(cfg App, projectID string, sessionID string) string {
	return filepath.Join(filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), projectID, "sessions"), sessionID)
}

func GlobalAuthConfigPath(cfg App) string {
	return filepath.Join(cfg.PersistenceRoot, globalAuthConfigName)
}

func ServerRPCURL(cfg App) string {
	return "ws://" + net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort)) + protocol.RPCPath
}

func ServerHTTPBaseURL(cfg App) string {
	return "http://" + net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort))
}
