package authui

import (
	"context"
	"errors"
	"testing"

	"core/server/auth"
)

func TestSlashCommandName(t *testing.T) {
	tests := []struct {
		name    string
		loader  AuthStateLoader
		want    string
		wantErr bool
	}{
		{name: "missing auth", want: "login"},
		{
			name: "api key",
			loader: staticStateLoader{state: auth.State{Method: auth.Method{
				Type:   auth.MethodAPIKey,
				APIKey: &auth.APIKeyMethod{Key: "sk-test"},
			}}},
			want: "login",
		},
		{
			name: "oauth",
			loader: staticStateLoader{state: auth.State{Method: auth.Method{
				Type: auth.MethodOAuth,
				OAuth: &auth.OAuthMethod{
					AccessToken: "access-token",
					TokenType:   "Bearer",
				},
			}}},
			want: "logout",
		},
		{
			name:    "load error",
			loader:  errorStateLoader{err: errors.New("permission denied")},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AuthSlashCommandName(context.Background(), tt.loader)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("AuthSlashCommandName returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("AuthSlashCommandName = %q, want %q", got, tt.want)
			}
		})
	}
}

type staticStateLoader struct {
	state auth.State
}

func (l staticStateLoader) Load(context.Context) (auth.State, error) {
	return l.state, nil
}

type errorStateLoader struct {
	err error
}

func (l errorStateLoader) Load(context.Context) (auth.State, error) {
	return auth.State{}, l.err
}
