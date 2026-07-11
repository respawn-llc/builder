package config

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel and typed errors for configuration validation and loading. These let
// callers (and tests) match failures with errors.Is / errors.As instead of
// matching on error-message wording. Producers wrap these with %w while keeping
// human-readable context (offending field, value, computed bounds, etc.).
var (
	// errProtectedPersistenceRoot is returned when a Go test binary attempts to
	// use a real, non-isolated persistence root.
	errProtectedPersistenceRoot = newConfigError("refusing to use protected persistence root")

	// errSettingsFileAlreadyExists is returned by onboarding when a settings
	// file is already present and would be overwritten.
	errSettingsFileAlreadyExists = newConfigError("settings file already exists")

	// errInvalidSubagentKey is returned when a subagents table key does not
	// normalize to a usable role name (e.g. a reserved name).
	errInvalidSubagentKey = newConfigError("invalid subagents key")

	// errSubagentRole wraps any failure parsing or validating a single subagent
	// role; the role key is preserved in the wrapped message.
	errSubagentRole = newConfigError("invalid subagent role")

	// errPersistenceRootInConfigFile is returned when a config.toml still
	// declares the removed persistence_root setting. The config+data root is
	// set with the --persistence-root flag or the KENT_PERSISTENCE_ROOT env var.
	errPersistenceRootInConfigFile = newConfigError("persistence_root is no longer a config.toml setting; set the config and data root with the --persistence-root flag or the KENT_PERSISTENCE_ROOT environment variable")

	// errSubagentDescriptionTooLong is returned when a subagent description
	// exceeds the maximum length after normalization.
	errSubagentDescriptionTooLong = newConfigError("subagent description too long")

	// Validation-rule sentinels for individual settings fields.
	errProviderOverrideRequiresModel      = newConfigError("provider_override requires an explicit model override")
	errInvalidProviderOverride            = newConfigError("invalid provider_override")
	errOpenAIBaseURLConflict              = newConfigError("provider_override conflicts with openai_base_url")
	errProviderCapabilitiesNeedID         = newConfigError("provider_capabilities.provider_id must not be empty when provider capability overrides are set")
	errReviewerProviderCapabilitiesNeedID = newConfigError("reviewer.provider_capabilities.provider_id must not be empty when reviewer provider capability overrides are set")
	errInvalidModelVerbosity              = newConfigError("invalid model_verbosity")
	errInvalidReviewerProvider            = newConfigError("invalid reviewer.provider_override")
	errReviewerContextWindowNegative      = newConfigError("reviewer.model_context_window must be >= 0")
	errModelContextWindowBelowMinimum     = newConfigError("model context window below minimum")
	errInvalidCacheWarningMode            = newConfigError("invalid cache_warning_mode")

	// errCompactionThresholdBelowMinimum is returned when the configured
	// compaction threshold falls below the minimum percentage of the model
	// context window.
	errCompactionThresholdBelowMinimum = newConfigError("context_compaction_threshold_tokens below minimum window percent")

	// errPreSubmitThresholdBelowMinimum is returned when the pre-submit
	// compaction lead pushes the effective threshold below the minimum.
	errPreSubmitThresholdBelowMinimum = newConfigError("pre-submit compaction threshold below minimum window percent")

	// Workflow validation sentinels.
	errInvalidWorkflowSettings = newConfigError("invalid workflow settings")
	errWorkflowConcurrency     = newConfigError("workflow.concurrency must be > 0")
)

// configError is a comparable sentinel error type for config failures. It is
// used with errors.Is via wrapping.
type configError struct {
	msg string
}

func newConfigError(msg string) *configError {
	return &configError{msg: msg}
}

func (e *configError) Error() string {
	return e.msg
}

func IsModelContextWindowBelowMinimum(err error) bool {
	return errors.Is(err, errModelContextWindowBelowMinimum)
}

func IsSettingsFileAlreadyExists(err error) bool {
	return errors.Is(err, errSettingsFileAlreadyExists)
}

// UnknownSettingsKeysError reports settings keys that are not recognized. The
// offending keys are exposed so callers can match structurally with errors.As.
type UnknownSettingsKeysError struct {
	Keys []string
}

func (e *UnknownSettingsKeysError) Error() string {
	return fmt.Sprintf("unknown settings key(s): %s", strings.Join(e.Keys, ", "))
}

// SettingsKeyTypeError reports a settings key whose TOML value has the wrong
// type. The dotted key path and expected type are exposed for structural
// matching.
type SettingsKeyTypeError struct {
	Key          string
	ExpectedType string
}

func (e *SettingsKeyTypeError) Error() string {
	return fmt.Sprintf("invalid settings key %s: expected %s", e.Key, e.ExpectedType)
}

// DuplicateSettingsKeysError reports two settings keys that collapse to the same
// normalized form. The original keys and normalized form are exposed.
type DuplicateSettingsKeysError struct {
	Scope        string
	SettingsPath string
	KeyA         string
	KeyB         string
	Normalized   string
}

func (e *DuplicateSettingsKeysError) Error() string {
	return fmt.Sprintf(
		"duplicate %s keys in %s: %q and %q both normalize to %q",
		e.Scope, e.SettingsPath, e.KeyA, e.KeyB, e.Normalized,
	)
}
