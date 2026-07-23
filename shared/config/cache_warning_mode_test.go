package config

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateCacheWarningMode(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.CacheWarningMode = CacheWarningMode("loud")
	err := configRegistry.validate(settingsState{Settings: settings}, map[string]string{"model": "default"})
	if !errors.Is(err, errInvalidCacheWarningMode) {
		t.Fatalf("expected cache_warning_mode validation error, got %v", err)
	}
}

func TestDefaultSettingsTOMLIncludesCacheWarningMode(t *testing.T) {
	if !strings.Contains(settingsTOMLWithRenderingOptions(configRegistry.defaultState().Settings, true, nil, nil), "cache_warning_mode = \"default\"") {
		t.Fatalf("default settings TOML did not include cache_warning_mode")
	}
}
