package blackbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessEnvironmentsAreSeparateAndFallible(t *testing.T) {
	root := t.TempDir()
	client, err := clientEnvironment(filepath.Join(root, "client"), root, "127.0.0.1", 7777)
	if err != nil {
		t.Fatalf("clientEnvironment: %v", err)
	}
	server, err := serverEnvironment(filepath.Join(root, "server"), root, "127.0.0.1", 7777, "http://127.0.0.1:9999/v1")
	if err != nil {
		t.Fatalf("serverEnvironment: %v", err)
	}
	clientValues := environmentValues(t, client)
	serverValues := environmentValues(t, server)
	if clientValues["TERM"] != "xterm-256color" || clientValues["LANG"] != "C.UTF-8" || clientValues["LC_ALL"] != "C.UTF-8" {
		t.Fatalf("client terminal environment = %#v", clientValues)
	}
	if _, exists := serverValues["TERM"]; exists {
		t.Fatalf("server inherited client terminal environment: %#v", serverValues)
	}
	if _, exists := clientValues["KENT_OPENAI_BASE_URL"]; exists {
		t.Fatalf("client received server-only model endpoint: %#v", clientValues)
	}
	if serverValues["KENT_OPENAI_BASE_URL"] != "http://127.0.0.1:9999/v1" {
		t.Fatalf("server model endpoint = %q", serverValues["KENT_OPENAI_BASE_URL"])
	}
	if _, err := os.Stat(clientValues["HOME"]); err != nil {
		t.Fatalf("client HOME was not created: %v", err)
	}
}

func environmentValues(t *testing.T, environment []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("malformed environment entry %q", entry)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate environment key %q", key)
		}
		values[key] = value
	}
	return values
}
