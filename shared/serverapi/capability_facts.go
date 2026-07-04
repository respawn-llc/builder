package serverapi

import (
	"errors"
	"strings"
)

var ErrUnsupportedProvider = errors.New("unsupported llm provider")

type CapabilityFactsRequest struct {
	WorkspaceRoot          *string  `json:"workspace_root,omitempty"`
	ExplicitLLMProviderIDs []string `json:"explicit_llm_provider_ids,omitempty"`
}

func (r CapabilityFactsRequest) Validate() error {
	if r.WorkspaceRoot != nil && strings.TrimSpace(*r.WorkspaceRoot) == "" {
		return errors.New("workspace_root must be a non-blank path when supplied")
	}
	for _, providerID := range r.ExplicitLLMProviderIDs {
		if strings.TrimSpace(providerID) == "" {
			return errors.New("explicit_llm_provider_ids must not contain blank provider ids")
		}
	}
	return nil
}

func (r CapabilityFactsRequest) NormalizedExplicitLLMProviderIDs() []string {
	ids := make([]string, 0, len(r.ExplicitLLMProviderIDs))
	seen := map[string]struct{}{}
	for _, providerID := range r.ExplicitLLMProviderIDs {
		trimmed := strings.TrimSpace(providerID)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, trimmed)
	}
	return ids
}

type CapabilityFactsResponse struct {
	Models          ModelCapabilityFacts          `json:"models"`
	Providers       ProviderCapabilityFacts       `json:"providers"`
	Imports         ImportCapabilityFacts         `json:"imports"`
	Defaults        CapabilityDefaultFacts        `json:"defaults"`
	Recommendations CapabilityRecommendationFacts `json:"recommendations"`
}

type ModelCapabilityFacts struct {
	KnownModels     []ModelCapabilityFact `json:"known_models"`
	UnknownFallback ModelCapabilityFact   `json:"unknown_fallback"`
}

type ModelCapabilityFact struct {
	ModelID                  *string               `json:"model_id,omitempty"`
	Known                    bool                  `json:"known"`
	ContextWindowTokens      *int                  `json:"context_window_tokens,omitempty"`
	LargeWindow              *ModelLargeWindowFact `json:"large_window,omitempty"`
	DefaultContextWindowMode *string               `json:"default_context_window_mode,omitempty"`
	SupportsThinking         bool                  `json:"supports_thinking"`
	SupportedThinkingLevels  []string              `json:"supported_thinking_levels,omitempty"`
	SupportsReasoningSummary bool                  `json:"supports_reasoning_summary"`
	SupportsVisionInputs     bool                  `json:"supports_vision_inputs"`
	Verbosity                ModelVerbosityFact    `json:"verbosity"`
}

type ModelLargeWindowFact struct {
	Tokens int `json:"tokens"`
}

type ModelVerbosityFact struct {
	Supported bool     `json:"supported"`
	Source    string   `json:"source"`
	Levels    []string `json:"levels,omitempty"`
}

type ProviderCapabilityFacts struct {
	CurrentEffective *LLMProviderCapabilityFact  `json:"current_effective,omitempty"`
	Explicit         []LLMProviderCapabilityFact `json:"explicit,omitempty"`
}

type LLMProviderCapabilityFact struct {
	LLMProviderID                 string `json:"llm_provider_id"`
	Role                          string `json:"role"`
	SupportsResponsesAPI          bool   `json:"supports_responses_api"`
	SupportsNativeCompaction      bool   `json:"supports_native_compaction"`
	SupportsInputTokenCount       bool   `json:"supports_input_token_count"`
	SupportsPromptCacheKey        bool   `json:"supports_prompt_cache_key"`
	SupportsNativeWebSearch       bool   `json:"supports_native_web_search"`
	SupportsReasoningEncryption   bool   `json:"supports_reasoning_encryption"`
	SupportsServerSideContextEdit bool   `json:"supports_server_side_context_edit"`
	IsOpenAIFirstParty            bool   `json:"is_openai_first_party"`
	SupportsProviderVerbosity     bool   `json:"supports_provider_verbosity"`
}

type ImportCapabilityFacts struct {
	Workspace       ImportWorkspaceFact             `json:"workspace"`
	Skills          ImportItemGroupFact             `json:"skills"`
	Commands        ImportItemGroupFact             `json:"commands"`
	SkillEnablement []SkillEnablementProjectionFact `json:"skill_enablement"`
	Errors          []ImportErrorFact               `json:"errors,omitempty"`
	Recommendations ImportRecommendationFacts       `json:"recommendations"`
}

type ImportWorkspaceFact struct {
	Root *string `json:"root,omitempty"`
}

type ImportItemGroupFact struct {
	Choices []ImportChoiceFact `json:"choices"`
	Roots   []ImportRootFact   `json:"roots"`
	Items   []ImportItemFact   `json:"items"`
	Target  ImportTargetFact   `json:"target"`
}

type ImportTargetFact struct {
	Skip      bool                 `json:"skip"`
	Conflicts []ImportConflictFact `json:"conflicts,omitempty"`
}

type ImportChoiceFact struct {
	Ref              ImportChoiceRef `json:"ref"`
	ImportProviderID *string         `json:"import_provider_id,omitempty"`
	SourceRootPath   *string         `json:"source_root_path,omitempty"`
	ItemCount        int             `json:"item_count"`
}

type ImportChoiceRef struct {
	Mode             string  `json:"mode"`
	SourceKind       *string `json:"source_kind,omitempty"`
	ImportProviderID *string `json:"import_provider_id,omitempty"`
	SourceRootPath   *string `json:"source_root_path,omitempty"`
}

type ImportRootFact struct {
	SourceKind       string  `json:"source_kind"`
	ImportProviderID *string `json:"import_provider_id,omitempty"`
	Path             string  `json:"path"`
	Exists           bool    `json:"exists"`
}

type ImportItemFact struct {
	Ref            ImportItemRef        `json:"ref"`
	Conflicts      []ImportConflictFact `json:"conflicts,omitempty"`
	DefaultEnabled *bool                `json:"default_enabled,omitempty"`
}

type ImportItemRef struct {
	ItemKind         string  `json:"item_kind"`
	SourceKind       string  `json:"source_kind"`
	ImportProviderID *string `json:"import_provider_id,omitempty"`
	SourceRootPath   *string `json:"source_root_path,omitempty"`
	SourcePath       *string `json:"source_path,omitempty"`
	TargetName       string  `json:"target_name"`
	Name             *string `json:"name,omitempty"`
	ModifiedUnixMs   *int64  `json:"modified_unix_ms,omitempty"`
}

type ImportConflictFact struct {
	SourceKind       string  `json:"source_kind"`
	ImportProviderID *string `json:"import_provider_id,omitempty"`
	Path             *string `json:"path,omitempty"`
}

type SkillEnablementProjectionFact struct {
	ChoiceRef  ImportChoiceRef  `json:"choice_ref"`
	Candidates []ImportItemFact `json:"candidates"`
}

type ImportErrorFact struct {
	Code             string  `json:"code"`
	Scope            string  `json:"scope"`
	ImportProviderID *string `json:"import_provider_id,omitempty"`
	Path             *string `json:"path,omitempty"`
	Operation        string  `json:"operation"`
	Message          string  `json:"message"`
}

type ImportRecommendationFacts struct {
	Skills   *ImportModeRecommendationFact `json:"skills,omitempty"`
	Commands *ImportModeRecommendationFact `json:"commands,omitempty"`
}

type ImportModeRecommendationFact struct {
	ChoiceRef   ImportChoiceRef `json:"choice_ref"`
	ItemCount   int             `json:"item_count"`
	SourcePaths []string        `json:"source_paths,omitempty"`
}

type CapabilityDefaultFacts struct {
	PrimaryModelID string                `json:"primary_model_id"`
	Thinking       ThinkingDefaultFact   `json:"thinking"`
	Verbosity      *VerbosityDefaultFact `json:"verbosity,omitempty"`
	CompactionMode string                `json:"compaction_mode"`
}

type ThinkingDefaultFact struct {
	Mode  string  `json:"mode"`
	Level *string `json:"level,omitempty"`
	Value *string `json:"value,omitempty"`
}

type VerbosityDefaultFact struct {
	Level string `json:"level"`
}

type CapabilityRecommendationFacts struct{}
