package chatcontext

import (
	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

// ResolvePolicy is the sole authority for the effective Context window,
// automatic threshold, and Compaction Mode after Agent-role settings and
// provider capabilities have been resolved.
func ResolvePolicy(
	settings config.Settings,
	effectiveCapabilities llm.ProviderCapabilities,
	locked *session.LockedContract,
) Policy {
	window := settings.ModelContextWindow
	if window <= 0 {
		window = config.DefaultOnboardingSettings().ModelContextWindow
	}
	capabilities := effectiveCapabilities
	if locked != nil {
		if lockedCapabilities, present := llm.ProviderCapabilitiesFromLocked(locked); present {
			capabilities = lockedCapabilities
		}
	}
	threshold := min(max(settings.ContextCompactionThresholdTokens, 0), window)
	return Policy{
		ContextWindowTokens:      int64(window),
		AutomaticThresholdTokens: int64(threshold),
		CompactionMode:           effectiveCompactionMode(settings.CompactionMode, capabilities),
	}
}

// ApplyPolicy makes the canonical policy authoritative in the settings passed
// to runtime activation without adding a parallel activation contract.
func ApplyPolicy(settings config.Settings, policy Policy) config.Settings {
	settings.ModelContextWindow = int(policy.ContextWindowTokens)
	settings.ContextCompactionThresholdTokens = int(policy.AutomaticThresholdTokens)
	switch policy.CompactionMode {
	case serverapi.ChatContextCompactionModeDisabled:
		settings.CompactionMode = config.CompactionModeNone
	case serverapi.ChatContextCompactionModeProviderNative:
		settings.CompactionMode = config.CompactionModeNative
	default:
		settings.CompactionMode = config.CompactionModeLocal
	}
	return settings
}

func effectiveCompactionMode(
	configured config.CompactionMode,
	capabilities llm.ProviderCapabilities,
) serverapi.ChatContextCompactionMode {
	switch configured {
	case config.CompactionModeNone:
		return serverapi.ChatContextCompactionModeDisabled
	case config.CompactionModeLocal:
		return serverapi.ChatContextCompactionModeLocal
	case config.CompactionModeNative:
		if capabilities.SupportsResponsesCompact {
			return serverapi.ChatContextCompactionModeProviderNative
		}
		return serverapi.ChatContextCompactionModeLocal
	default:
		if capabilities.SupportsResponsesCompact {
			return serverapi.ChatContextCompactionModeProviderNative
		}
		return serverapi.ChatContextCompactionModeLocal
	}
}
