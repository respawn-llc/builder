package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/prompts"
	"core/server/metadata"
	"core/server/session"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/toolspec"
)

type recordingGoalRemote struct {
	showReq          []serverapi.RuntimeGoalShowRequest
	setReq           []serverapi.RuntimeGoalSetRequest
	completeReq      []serverapi.RuntimeGoalStatusRequest
	goal             *serverapi.RuntimeGoal
	setErr           error
	showDeadline     time.Time
	completeDeadline time.Time
}

func (r *recordingGoalRemote) Close() error { return nil }

func (r *recordingGoalRemote) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.showReq = append(r.showReq, req)
	if deadline, ok := ctx.Deadline(); ok {
		r.showDeadline = deadline
	}
	return serverapi.RuntimeGoalShowResponse{Goal: r.goal}, nil
}

func (r *recordingGoalRemote) SetGoal(_ context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.setReq = append(r.setReq, req)
	if r.setErr != nil {
		return serverapi.RuntimeGoalShowResponse{}, r.setErr
	}
	return serverapi.RuntimeGoalShowResponse{Goal: r.goal}, nil
}

func (r *recordingGoalRemote) PauseGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}

func (r *recordingGoalRemote) ResumeGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}

func (r *recordingGoalRemote) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.completeReq = append(r.completeReq, req)
	if deadline, ok := ctx.Deadline(); ok {
		r.completeDeadline = deadline
	}
	return serverapi.RuntimeGoalShowResponse{Goal: r.goal}, nil
}

func (r *recordingGoalRemote) ClearGoal(context.Context, serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}

func TestGoalShowUsesSessionIDEnv(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	remote := &recordingGoalRemote{goal: &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship goal mode", Status: "active"}}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"show"}, stdout, stderr); code != 0 {
		t.Fatalf("goal show exit = %d stderr=%q", code, stderr.String())
	}
	if len(remote.showReq) != 1 || remote.showReq[0].SessionID != "session-1" {
		t.Fatalf("show requests = %+v", remote.showReq)
	}
	if !strings.Contains(stdout.String(), "ship goal mode") || !strings.Contains(stdout.String(), "active") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "goal-1") || strings.Contains(stdout.String(), "ID:") {
		t.Fatalf("plain goal show leaked goal id: %q", stdout.String())
	}
}

func TestGoalAgentEnvAllowsSetWithAgentActor(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	remote := &recordingGoalRemote{goal: &serverapi.RuntimeGoal{ID: "goal-1", Objective: "new goal", Status: "active"}}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"set", "new goal"}, stdout, stderr); code != 0 {
		t.Fatalf("goal set exit = %d stderr=%q", code, stderr.String())
	}
	if len(remote.setReq) != 1 {
		t.Fatalf("set requests = %+v", remote.setReq)
	}
	if remote.setReq[0].SessionID != "session-1" || remote.setReq[0].Actor != "agent" || remote.setReq[0].Objective != "new goal" {
		t.Fatalf("set request = %+v", remote.setReq[0])
	}
}

func TestGoalAgentEnvSetOverwritePrintsDeniedPrompt(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	existing := &serverapi.RuntimeGoal{ID: "goal-1", Objective: "existing goal", Status: "active"}
	remote := &recordingGoalRemote{
		goal:   existing,
		setErr: errors.New(strings.TrimSpace(prompts.RenderGoalAgentDuplicateSetDeniedPrompt(existing.Objective, existing.Status))),
	}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"set", "replacement goal"}, stdout, stderr); code == 0 {
		t.Fatalf("goal set overwrite exit = 0")
	}
	if strings.TrimSpace(stderr.String()) == "" {
		t.Fatalf("expected denial reason surfaced to stderr, got empty")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if len(remote.setReq) != 1 {
		t.Fatalf("set requests = %+v", remote.setReq)
	}
	if remote.goal != existing || remote.goal.Objective != "existing goal" {
		t.Fatalf("remote goal mutated = %+v", remote.goal)
	}
}

func TestGoalAgentEnvDeniesNonSetMutationWithoutDialing(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	remote := &recordingGoalRemote{}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"pause"}, new(strings.Builder), stderr); code == 0 {
		t.Fatalf("goal pause exit = 0")
	}
	if !strings.Contains(stderr.String(), prompts.RenderGoalAgentCommandDeniedPrompt()) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if len(remote.showReq) != 0 || len(remote.completeReq) != 0 || len(remote.setReq) != 0 {
		t.Fatalf("remote was called: %+v", remote)
	}
}

func TestGoalSetRejectsEmptyObjectiveBeforeDialing(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	remote := &recordingGoalRemote{}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"set", "--session", "session-1", "   "}, new(strings.Builder), stderr); code != 2 {
		t.Fatalf("goal set empty exit = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "goal set requires an objective") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if len(remote.setReq) != 0 {
		t.Fatalf("set called for empty objective: %+v", remote.setReq)
	}
}

func TestGoalAgentCompleteRequiresConfirmTripwire(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	remote := &recordingGoalRemote{goal: &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship goal mode", Status: "active"}}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"complete"}, new(strings.Builder), stderr); code == 0 {
		t.Fatalf("goal complete without confirm exit = 0")
	}
	if len(remote.completeReq) != 0 {
		t.Fatalf("complete called before confirm: %+v", remote.completeReq)
	}

	stdout := new(strings.Builder)
	stderr.Reset()
	if code := goalSubcommand([]string{"complete", "--confirm"}, stdout, stderr); code != 0 {
		t.Fatalf("goal complete --confirm exit = %d stderr=%q", code, stderr.String())
	}
	if len(remote.completeReq) != 1 {
		t.Fatalf("complete requests = %+v", remote.completeReq)
	}
	if remote.completeReq[0].SessionID != "session-1" || remote.completeReq[0].Actor != "agent" {
		t.Fatalf("complete req = %+v", remote.completeReq[0])
	}
}

func TestGoalCompleteAlreadyCompletePrintsAlreadyCompletePrompt(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "without confirm", args: []string{"complete"}},
		{name: "with confirm", args: []string{"complete", "--confirm"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(sessionenv.SessionIDEnv, "session-1")
			remote := &recordingGoalRemote{goal: &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship goal mode", Status: "complete"}}
			restore := replaceGoalCommandRemoteOpener(t, remote)
			defer restore()

			stdout := new(strings.Builder)
			stderr := new(strings.Builder)
			if code := goalSubcommand(tt.args, stdout, stderr); code != 0 {
				t.Fatalf("goal complete already-complete exit = %d stderr=%q", code, stderr.String())
			}
			if got, want := strings.TrimSpace(stdout.String()), prompts.RenderGoalAlreadyCompletePrompt("ship goal mode"); got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
			if len(remote.completeReq) != 0 {
				t.Fatalf("complete called for already-complete goal: %+v", remote.completeReq)
			}
			if len(remote.showReq) != 1 || remote.showReq[0].SessionID != "session-1" {
				t.Fatalf("show requests = %+v", remote.showReq)
			}
		})
	}
}

func TestGoalCompleteUsesFreshTimeoutForCompletionRPC(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-1")
	remote := &recordingGoalRemote{goal: &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship goal mode", Status: "active"}}
	restore := replaceGoalCommandRemoteOpener(t, remote)
	defer restore()

	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	if code := goalSubcommand([]string{"complete", "--confirm"}, stdout, stderr); code != 0 {
		t.Fatalf("goal complete --confirm exit = %d stderr=%q", code, stderr.String())
	}
	if remote.showDeadline.IsZero() || remote.completeDeadline.IsZero() {
		t.Fatalf("deadlines missing: show=%v complete=%v", remote.showDeadline, remote.completeDeadline)
	}
	if !remote.completeDeadline.After(remote.showDeadline) {
		t.Fatalf("complete deadline = %v, want fresh deadline after show deadline %v", remote.completeDeadline, remote.showDeadline)
	}
}

func TestGoalCommandSubprocessTargetsLiveSessionFromUnboundWorktree(t *testing.T) {
	kentPath := filepath.Join(t.TempDir(), "kent")
	buildCmd := exec.Command("go", "build", "-o", kentPath, ".")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build subprocess kent: %v\n%s", err, output)
	}

	home := t.TempDir()
	workspace := t.TempDir()
	unboundWorktree := t.TempDir()
	t.Setenv("HOME", home)
	configureBindingCommandTestServerPort(t)

	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	store, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if _, err := store.SetGoal("exercise live goal CLI", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	record, err := metadataStore.ResolvePersistedSession(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta == nil || record.Meta.Goal == nil {
		t.Fatalf("persisted goal metadata missing: %+v", record.Meta)
	}

	cleanup := startBindingCommandServer(t, unboundWorktree)
	defer cleanup()
	remote, err := client.DialConfiguredRemoteForProjectWorkspace(context.Background(), cfg, binding.ProjectID, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("DialConfiguredRemoteForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	settings := cfg.Settings
	settings.Model = "gpt-5"
	settings.ProviderOverride = "openai"
	if _, err := remote.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activate-goal-cli-e2e",
		SessionID:       store.Meta().SessionID,
		ActiveSettings:  settings,
		EnabledToolIDs:  toolIDsAsStrings(config.EnabledToolIDs(settings)),
		Source:          cfg.Source,
	}); err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	defer func() {
		_, _ = remote.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-goal-cli-e2e",
			SessionID:       store.Meta().SessionID,
		})
	}()

	t.Setenv(sessionenv.SessionIDEnv, store.Meta().SessionID)
	showOutput, showErr := runGoalCommandSubprocess(t, kentPath, unboundWorktree, store.Meta().SessionID, "show", "--json")
	if showErr != "" {
		t.Fatalf("goal show stderr = %q", showErr)
	}
	var show serverapi.RuntimeGoalShowResponse
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("decode show json: %v output=%q", err, showOutput)
	}
	if show.Goal == nil || show.Goal.Status != "active" || show.Goal.Objective != "exercise live goal CLI" {
		t.Fatalf("show goal = %+v", show.Goal)
	}

	overwriteOutput, overwriteErr, overwriteRunErr := runGoalCommandSubprocessRaw(t, kentPath, unboundWorktree, store.Meta().SessionID, "set", "replacement live goal CLI")
	if overwriteRunErr == nil {
		t.Fatalf("goal set overwrite unexpectedly succeeded stdout=%q stderr=%q", overwriteOutput, overwriteErr)
	}
	if overwriteOutput != "" {
		t.Fatalf("goal set overwrite stdout = %q, want empty", overwriteOutput)
	}
	record, err = metadataStore.ResolvePersistedSession(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession after rejected overwrite: %v", err)
	}
	if goal := record.Meta.Goal; goal == nil || goal.Objective != "exercise live goal CLI" || goal.Status != session.GoalStatusActive {
		t.Fatalf("persisted goal after rejected overwrite = %+v", goal)
	}

	if err := remote.SubmitUserShellCommand(context.Background(), serverapi.RuntimeSubmitUserShellCommandRequest{
		ClientRequestID: "shell-goal-complete-e2e",
		SessionID:       store.Meta().SessionID,
		Command:         shellQuote(kentPath) + " goal complete --confirm",
	}); err != nil {
		t.Fatalf("shell goal complete: %v", err)
	}
	record, err = metadataStore.ResolvePersistedSession(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession after complete: %v", err)
	}
	if record.Meta == nil {
		t.Fatal("persisted metadata missing after complete")
	}
	if goal := record.Meta.Goal; goal == nil || goal.Objective != "exercise live goal CLI" || goal.Status != session.GoalStatusComplete {
		t.Fatalf("persisted goal after complete = %+v", goal)
	}
}

func runGoalCommandSubprocess(t *testing.T, kentPath string, workdir string, sessionID string, args ...string) (stdout string, stderr string) {
	t.Helper()
	stdout, stderr, err := runGoalCommandSubprocessRaw(t, kentPath, workdir, sessionID, args...)
	if err != nil {
		t.Fatalf("%s goal %s failed: %v stdout=%q stderr=%q", kentPath, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

func runGoalCommandSubprocessRaw(t *testing.T, kentPath string, workdir string, sessionID string, args ...string) (stdout string, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(kentPath, append([]string{"goal"}, args...)...)
	cmd.Dir = workdir
	cmd.Env = goalCommandSubprocessEnv(sessionID)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

func goalCommandSubprocessEnv(sessionID string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "KENT_SESSION_ID=") {
			continue
		}
		env = append(env, item)
	}
	if strings.TrimSpace(sessionID) != "" {
		env = append(env, "KENT_SESSION_ID="+sessionID)
	}
	return env
}

func toolIDsAsStrings(ids []toolspec.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func replaceGoalCommandRemoteOpener(t *testing.T, remote *recordingGoalRemote) func() {
	t.Helper()
	previous := goalCommandRemoteOpener
	goalCommandRemoteOpener = func(context.Context) (goalCommandRemote, error) {
		return remote, nil
	}
	return func() { goalCommandRemoteOpener = previous }
}
