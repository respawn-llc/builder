package authservice

import (
	"testing"

	"core/shared/config"
)

func TestStartupAuthRequired(t *testing.T) {
	tests := []struct {
		name     string
		settings config.Settings
		want     bool
	}{
		{
			name:     "openai model requires auth",
			settings: config.Settings{Model: "gpt-5"},
			want:     true,
		},
		{
			name:     "explicit base url disables startup auth gate",
			settings: config.Settings{Model: "gpt-5", OpenAIBaseURL: "http://127.0.0.1:8080/v1"},
			want:     false,
		},
		{
			name:     "explicit default openai base url still requires auth",
			settings: config.Settings{Model: "gpt-5", OpenAIBaseURL: "https://api.openai.com/v1/"},
			want:     true,
		},
		{
			name:     "explicit bare openai host still requires auth",
			settings: config.Settings{Model: "gpt-5", OpenAIBaseURL: "https://api.openai.com"},
			want:     true,
		},
		{
			name:     "explicit anthropic override disables startup auth gate",
			settings: config.Settings{ProviderOverride: "anthropic", Model: "claude-3-7-sonnet"},
			want:     false,
		},
		{
			name:     "unknown custom model does not force startup auth",
			settings: config.Settings{Model: "my-local-alias"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StartupAuthRequired(tt.settings); got != tt.want {
				t.Fatalf("StartupAuthRequired() = %t, want %t", got, tt.want)
			}
		})
	}
}
