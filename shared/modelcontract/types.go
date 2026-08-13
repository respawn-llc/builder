package modelcontract

type ModelMetadata struct {
	ContextWindowTokens      int
	LargeContextWindowTokens int
}

type ProviderCapabilities struct {
	ProviderID                    string
	SupportsResponsesAPI          bool
	SupportsResponsesCompact      bool
	SupportsPromptCacheKey        bool
	SupportsNativeWebSearch       bool
	SupportsReasoningEncrypted    bool
	SupportsServerSideContextEdit bool
	SupportsProviderVerbosity     bool
	IsOpenAIFirstParty            bool
}
