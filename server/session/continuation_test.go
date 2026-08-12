package session

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/textutil"
)

func TestContinuationRolePersistence(t *testing.T) {
	worker := "worker"
	fast := "fast"
	tests := []struct {
		name     string
		payload  string
		wantRole *string
		wantErr  bool
	}{
		{name: "omitted default role", payload: `{}`},
		{name: "null default role", payload: `{"agent_role":null}`},
		{name: "custom role", payload: `{"agent_role":" Worker "}`, wantRole: &worker},
		{name: "fast role", payload: `{"agent_role":"fast"}`, wantRole: &fast},
		{name: "empty role", payload: `{"agent_role":""}`, wantErr: true},
		{name: "whitespace role", payload: `{"agent_role":" \t "}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var context ContinuationContext
			if err := json.Unmarshal([]byte(tt.payload), &context); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{}}
			store, err := Create(t.TempDir(), "workspace", t.TempDir(), testSessionCategory, persistence.options()...)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			err = store.SetContinuationContext(context)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidContinuationAgentRole) {
					t.Fatalf("SetContinuationContext error = %v, want invalid role", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetContinuationContext: %v", err)
			}
			reopened, err := Open(store.Dir(), persistence.options()...)
			if err != nil {
				t.Fatalf("reopen session: %v", err)
			}
			got := reopened.Meta().Continuation
			if tt.wantRole == nil {
				if got != nil && got.AgentRole != nil {
					t.Fatalf("continuation = %+v, want absent role", got)
				}
				return
			}
			if got == nil || got.AgentRole == nil || *got.AgentRole != *tt.wantRole {
				t.Fatalf("continuation = %+v, want role %q", got, *tt.wantRole)
			}
		})
	}
}

func TestNormalizeContinuationContextRole(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		wantErr bool
	}{
		{name: "omitted role", payload: `{}`},
		{name: "null role", payload: `{"agent_role":null}`},
		{name: "normalizes custom role", payload: `{"agent_role":" Worker "}`, want: "worker"},
		{name: "preserves fast role", payload: `{"agent_role":"fast"}`, want: "fast"},
		{name: "rejects empty role", payload: `{"agent_role":""}`, wantErr: true},
		{name: "rejects whitespace role", payload: `{"agent_role":" \t "}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoded ContinuationContext
			if err := json.Unmarshal([]byte(tt.payload), &decoded); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			got, err := NormalizeContinuationContext(decoded)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected invalid continuation role error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize continuation: %v", err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("continuation = %+v, want absent", got)
				}
				return
			}
			if got == nil || got.AgentRole == nil {
				t.Fatalf("continuation = %+v, want present role", got)
			}
			if *got.AgentRole != tt.want {
				t.Fatalf("agent role = %q, want %q", *got.AgentRole, tt.want)
			}
		})
	}
}

func TestNormalizeContinuationContextOpenAIBaseURLUsesTypedAbsence(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    *string
		wantErr bool
	}{
		{name: "omitted", payload: `{}`},
		{name: "null", payload: `{"openai_base_url":null}`},
		{name: "present", payload: `{"openai_base_url":" https://example.test/v1 "}`, want: textutil.Value("https://example.test/v1")},
		{name: "empty", payload: `{"openai_base_url":""}`, wantErr: true},
		{name: "blank", payload: `{"openai_base_url":" \t "}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoded ContinuationContext
			if err := json.Unmarshal([]byte(test.payload), &decoded); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			normalized, err := NormalizeContinuationContext(decoded)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidContinuationOpenAIBaseURL) {
					t.Fatalf("NormalizeContinuationContext error = %v, want invalid OpenAI base URL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeContinuationContext: %v", err)
			}
			if test.want == nil {
				if normalized != nil {
					t.Fatalf("normalized continuation = %+v, want absent", normalized)
				}
				return
			}
			if normalized == nil || normalized.OpenAIBaseURL == nil || *normalized.OpenAIBaseURL != *test.want {
				t.Fatalf("normalized continuation = %+v, want OpenAI base URL %q", normalized, *test.want)
			}
		})
	}
}

func TestContinuationContextPersistsAbsentOpenAIBaseURLAsNull(t *testing.T) {
	encoded, err := json.Marshal(ContinuationContext{AgentRole: textutil.Value("worker")})
	if err != nil {
		t.Fatalf("marshal continuation: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("decode continuation: %v", err)
	}
	baseURL, exists := persisted["openai_base_url"]
	if !exists || baseURL != nil {
		t.Fatalf("persisted openai_base_url = %#v, present=%t; want explicit null", baseURL, exists)
	}
}
