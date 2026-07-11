package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestContinuationRoleFilePersistence(t *testing.T) {
	worker := "worker"
	fast := "fast"
	tests := []struct {
		name        string
		payload     string
		wantRole    *string
		wantRoleKey bool
		wantErr     bool
	}{
		{name: "omitted default role", payload: `{}`, wantRoleKey: false},
		{name: "null default role", payload: `{"agent_role":null}`, wantRoleKey: false},
		{name: "custom role", payload: `{"agent_role":" Worker "}`, wantRole: &worker, wantRoleKey: true},
		{name: "fast role", payload: `{"agent_role":"fast"}`, wantRole: &fast, wantRoleKey: true},
		{name: "empty role", payload: `{"agent_role":""}`, wantErr: true},
		{name: "whitespace role", payload: `{"agent_role":" \t "}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var context ContinuationContext
			if err := json.Unmarshal([]byte(tt.payload), &context); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			store, err := Create(t.TempDir(), "workspace", t.TempDir())
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
			data, err := os.ReadFile(filepath.Join(store.Dir(), sessionFile))
			if err != nil {
				t.Fatalf("read metadata: %v", err)
			}
			var encoded struct {
				Continuation map[string]json.RawMessage `json:"continuation"`
			}
			if err := json.Unmarshal(data, &encoded); err != nil {
				t.Fatalf("decode persisted metadata: %v", err)
			}
			_, hasRoleKey := encoded.Continuation["agent_role"]
			if hasRoleKey != tt.wantRoleKey {
				t.Fatalf("persisted agent_role key = %t, want %t", hasRoleKey, tt.wantRoleKey)
			}
			reopened, err := Open(store.Dir())
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
