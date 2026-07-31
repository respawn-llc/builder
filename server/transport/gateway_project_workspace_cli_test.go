package transport

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	remoteclient "core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestProjectWorkspaceDetachThroughCLIAndGatewayAcrossWorkingDirectories(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	buildEnv := os.Environ()
	serverCWD := t.TempDir()
	t.Chdir(serverCWD)
	appCore, server := newUnboundGatewayTestServer(t)

	clientCWD := t.TempDir()
	if filepath.Clean(appCore.Config().WorkspaceRoot) == filepath.Clean(clientCWD) {
		t.Fatal("server and client working directories unexpectedly match")
	}
	sharedRelativePath := filepath.Join("shared", "workspace")
	sharedRoot := filepath.Join(clientCWD, sharedRelativePath)
	if err := os.MkdirAll(sharedRoot, 0o755); err != nil {
		t.Fatalf("create shared workspace: %v", err)
	}
	projectARoot := filepath.Join(clientCWD, "project-a")
	projectBRoot := filepath.Join(clientCWD, "project-b")
	for _, root := range []string{projectARoot, projectBRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create project workspace %q: %v", root, err)
		}
	}

	parsedServerURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse gateway URL: %v", err)
	}
	serverHost, serverPort, err := net.SplitHostPort(parsedServerURL.Host)
	if err != nil {
		t.Fatalf("split gateway address: %v", err)
	}
	kentBinary := strings.TrimSpace(os.Getenv("KENT_PTY_KENT_BINARY"))
	if kentBinary == "" {
		kentBinary = filepath.Join(t.TempDir(), "kent")
		build := exec.Command("go", "build", "-o", kentBinary, "./cli/kent")
		build.Dir = repoRoot
		build.Env = buildEnv
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build CLI: %v\n%s", err, output)
		}
	} else if _, err := os.Stat(kentBinary); err != nil {
		t.Fatalf("prebuilt CLI binary %q: %v", kentBinary, err)
	}

	baseEnv := filteredCLIEnvironment(os.Environ())
	baseEnv = append(baseEnv,
		"HOME="+os.Getenv("HOME"),
		"KENT_PERSISTENCE_ROOT="+appCore.Config().PersistenceRoot,
		"KENT_SERVER_HOST="+serverHost,
		"KENT_SERVER_PORT="+serverPort,
	)
	runKent := func(args ...string) (string, string, int) {
		command := exec.Command(kentBinary, args...)
		command.Dir = clientCWD
		command.Env = baseEnv
		var stdout, stderr strings.Builder
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		if err == nil {
			return stdout.String(), stderr.String(), 0
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), exitErr.ExitCode()
		}
		t.Fatalf("run kent %q: %v", args, err)
		return "", "", 1
	}

	projectAOutput, stderr, exitCode := runKent("project", "create", "--name", "Project A", "--path", projectARoot)
	if exitCode != 0 {
		t.Fatalf("create project A: exit=%d stderr=%q", exitCode, stderr)
	}
	projectA := strings.TrimSpace(projectAOutput)
	projectBOutput, stderr, exitCode := runKent("project", "create", "--name", "Project B", "--path", projectBRoot)
	if exitCode != 0 {
		t.Fatalf("create project B: exit=%d stderr=%q", exitCode, stderr)
	}
	projectB := strings.TrimSpace(projectBOutput)
	if projectA == "" || projectB == "" || projectA == projectB {
		t.Fatalf("created project IDs = %q/%q, want distinct non-blank IDs", projectA, projectB)
	}

	for _, projectID := range []string{projectA, projectB} {
		_, stderr, exitCode = runKent("attach", "--project", projectID, sharedRelativePath)
		if exitCode != 0 {
			t.Fatalf("attach shared workspace to %s: exit=%d stderr=%q", projectID, exitCode, stderr)
		}
	}
	if _, _, exitCode = runKent("project", sharedRelativePath); exitCode != 1 {
		t.Fatalf("ambiguous project lookup exit=%d, want 1", exitCode)
	}

	parsedServerURL.Scheme = "ws"
	rpcURL := parsedServerURL.String()
	remote, err := remoteclient.DialRemoteURL(context.Background(), rpcURL)
	if err != nil {
		t.Fatalf("dial gateway for workspace identity: %v", err)
	}
	defer func() { _ = remote.Close() }()
	canonicalSharedRoot, err := config.CanonicalWorkspaceRoot(sharedRoot)
	if err != nil {
		t.Fatalf("canonicalize shared workspace: %v", err)
	}
	workspaces, err := remote.ListProjectWorkspaces(context.Background(), serverapi.ProjectWorkspaceListRequest{
		ProjectID: projectB,
		PageSize:  100,
	})
	if err != nil {
		t.Fatalf("list Project B workspaces: %v", err)
	}
	var detachedWorkspaceID string
	for _, workspace := range workspaces.Workspaces {
		if workspace.RootPath == canonicalSharedRoot {
			detachedWorkspaceID = workspace.WorkspaceID
			break
		}
	}
	if detachedWorkspaceID == "" {
		t.Fatalf("Project B workspace list = %+v, want shared workspace %q", workspaces.Workspaces, canonicalSharedRoot)
	}

	detachedOutput, stderr, exitCode := runKent("detach", "--project", projectB, sharedRelativePath)
	if exitCode != 0 {
		t.Fatalf("detach project B: exit=%d stderr=%q", exitCode, stderr)
	}
	if detachedOutput != detachedWorkspaceID+"\n" {
		t.Fatalf("detach output = %q, want exactly %q", detachedOutput, detachedWorkspaceID+"\n")
	}
	remainingOutput, stderr, exitCode := runKent("project", sharedRelativePath)
	if exitCode != 0 || strings.TrimSpace(remainingOutput) != projectA {
		t.Fatalf("remaining project lookup: exit=%d stdout=%q stderr=%q, want %s", exitCode, remainingOutput, stderr, projectA)
	}
}

func filteredCLIEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "KENT_SESSION_ID", "KENT_RUN_ID", "KENT_STEP_ID", "KENT_SERVER_HOST", "KENT_SERVER_PORT", "KENT_PERSISTENCE_ROOT":
			continue
		default:
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
