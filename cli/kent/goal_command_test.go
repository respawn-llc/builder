package main

import (
	"bufio"
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
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/sessionenv"
	"core/shared/toolspec"
)

type recordingGoalRemote struct {
	showReq          []serverapi.RuntimeGoalShowRequest
	setReq           []serverapi.RuntimeGoalSetRequest
	pauseReq         []serverapi.RuntimeGoalStatusRequest
	resumeReq        []serverapi.RuntimeGoalStatusRequest
	completeReq      []serverapi.RuntimeGoalStatusRequest
	clearReq         []serverapi.RuntimeGoalClearRequest
	goal             *serverapi.RuntimeGoal
	setErr           error
	pauseErr         error
	resumeErr        error
	completeErr      error
	clearErr         error
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

func (r *recordingGoalRemote) PauseGoal(_ context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.pauseReq = append(r.pauseReq, req)
	return serverapi.RuntimeGoalShowResponse{}, r.pauseErr
}

func (r *recordingGoalRemote) ResumeGoal(_ context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.resumeReq = append(r.resumeReq, req)
	return serverapi.RuntimeGoalShowResponse{}, r.resumeErr
}

func (r *recordingGoalRemote) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.completeReq = append(r.completeReq, req)
	if deadline, ok := ctx.Deadline(); ok {
		r.completeDeadline = deadline
	}
	if r.completeErr != nil {
		return serverapi.RuntimeGoalShowResponse{}, r.completeErr
	}
	return serverapi.RuntimeGoalShowResponse{Goal: r.goal}, nil
}

func (r *recordingGoalRemote) ClearGoal(_ context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	r.clearReq = append(r.clearReq, req)
	return serverapi.RuntimeGoalShowResponse{}, r.clearErr
}

func TestGoalMutationRuntimeUnavailableMapsToTypedPresentationError(t *testing.T) {
	const sessionID = "cc948e1e-17e5-4213-87d5-4793ebe18a55"
	transportErr := errors.Join(serverapi.ErrRuntimeUnavailable, errors.New("transport detail that must not be rendered"))

	err := goalMutationCommandError(sessionID, transportErr)
	var presentationErr goalRuntimeUnavailablePresentationError
	if !errors.As(err, &presentationErr) {
		t.Fatalf("goalMutationCommandError type = %T, want goalRuntimeUnavailablePresentationError", err)
	}
	if presentationErr.SessionID != sessionID {
		t.Fatalf("presentation session id = %q, want %q", presentationErr.SessionID, sessionID)
	}
	if errors.Is(err, transportErr) {
		t.Fatal("presentation error must discard the joined transport error")
	}
	ordinaryErr := errors.New("ordinary mutation failure")
	if got := goalMutationCommandError(sessionID, ordinaryErr); got != ordinaryErr {
		t.Fatalf("ordinary error = %v, want original error identity", got)
	}
}

func TestGoalRuntimeBackedMutationsPresentRuntimeUnavailableForResolvedSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	const sessionID = "cc948e1e-17e5-4213-87d5-4793ebe18a55"
	runtimeErr := errors.Join(serverapi.ErrRuntimeUnavailable, errors.New("joined transport detail"))
	tests := []struct {
		name      string
		args      []string
		goal      *serverapi.RuntimeGoal
		configure func(*recordingGoalRemote)
		callCount func(*recordingGoalRemote) int
	}{
		{
			name:      "set",
			args:      []string{"set", "--session", sessionID, "replacement goal"},
			configure: func(remote *recordingGoalRemote) { remote.setErr = runtimeErr },
			callCount: func(remote *recordingGoalRemote) int { return len(remote.setReq) },
		},
		{
			name:      "pause",
			args:      []string{"pause", "--session", sessionID},
			configure: func(remote *recordingGoalRemote) { remote.pauseErr = runtimeErr },
			callCount: func(remote *recordingGoalRemote) int { return len(remote.pauseReq) },
		},
		{
			name:      "resume",
			args:      []string{"resume", "--session", sessionID},
			configure: func(remote *recordingGoalRemote) { remote.resumeErr = runtimeErr },
			callCount: func(remote *recordingGoalRemote) int { return len(remote.resumeReq) },
		},
		{
			name:      "complete active",
			args:      []string{"complete", "--session", sessionID},
			goal:      &serverapi.RuntimeGoal{ID: "goal-active", Objective: "dormant goal", Status: string(session.GoalStatusActive)},
			configure: func(remote *recordingGoalRemote) { remote.completeErr = runtimeErr },
			callCount: func(remote *recordingGoalRemote) int { return len(remote.completeReq) },
		},
		{
			name:      "complete paused",
			args:      []string{"complete", "--session", sessionID},
			goal:      &serverapi.RuntimeGoal{ID: "goal-paused", Objective: "dormant goal", Status: string(session.GoalStatusPaused)},
			configure: func(remote *recordingGoalRemote) { remote.completeErr = runtimeErr },
			callCount: func(remote *recordingGoalRemote) int { return len(remote.completeReq) },
		},
		{
			name:      "clear",
			args:      []string{"clear", "--session", sessionID},
			configure: func(remote *recordingGoalRemote) { remote.clearErr = runtimeErr },
			callCount: func(remote *recordingGoalRemote) int { return len(remote.clearReq) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := &recordingGoalRemote{goal: tt.goal}
			tt.configure(remote)
			restore := replaceGoalCommandRemoteOpener(t, remote)
			defer restore()

			stdout := new(strings.Builder)
			stderr := new(strings.Builder)
			if code := goalSubcommand(tt.args, stdout, stderr); code != 1 {
				t.Fatalf("goal mutation exit = %d, want 1", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if strings.TrimSpace(stderr.String()) == "" {
				t.Fatal("stderr is empty, want presentation error")
			}
			if got := tt.callCount(remote); got != 1 {
				t.Fatalf("mutation RPC calls = %d, want 1", got)
			}
		})
	}
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
	t.Setenv(sessionenv.RunIDEnv, "run-1")
	t.Setenv(sessionenv.StepIDEnv, "step-1")
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
	if remote.setReq[0].SessionID != "session-1" || remote.setReq[0].Actor != "agent" || remote.setReq[0].Objective != "new goal" || remote.setReq[0].RunID != "run-1" || remote.setReq[0].StepID != "step-1" {
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
			remote := &recordingGoalRemote{
				goal:        &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship goal mode", Status: "complete"},
				completeErr: errors.Join(serverapi.ErrRuntimeUnavailable, errors.New("dormant runtime")),
			}
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

func TestGoalCommandSubprocessReadsDormantAndMutatesLiveSessionFromUnboundWorktree(t *testing.T) {
	kentPath := filepath.Join(t.TempDir(), "kent")
	buildCmd := exec.Command("go", "build", "-o", kentPath, ".")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build subprocess kent: %v\n%s", err, output)
	}

	home := t.TempDir()
	workspace := t.TempDir()
	unboundWorktree := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_PERSISTENCE_ROOT", filepath.Join(home, ".kent"))
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
		cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
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
	t.Setenv(sessionenv.SessionIDEnv, store.Meta().SessionID)

	assertGoalCommandSubprocessShow := func(want *session.GoalState) {
		t.Helper()
		showOutput, showErr := runGoalCommandSubprocess(t, kentPath, unboundWorktree, store.Meta().SessionID, "show", "--json")
		if showErr != "" {
			t.Fatalf("goal show stderr = %q", showErr)
		}
		var show serverapi.RuntimeGoalShowResponse
		if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
			t.Fatalf("decode show json: %v output=%q", err, showOutput)
		}
		if show.Goal == nil ||
			show.Goal.ID != want.ID ||
			show.Goal.Status != string(want.Status) ||
			show.Goal.Objective != want.Objective ||
			!show.Goal.CreatedAt.Equal(want.CreatedAt) ||
			!show.Goal.UpdatedAt.Equal(want.UpdatedAt) {
			t.Fatalf("show goal = %+v, want %+v", show.Goal, want)
		}
		var payload struct {
			Goal map[string]json.RawMessage `json:"goal"`
		}
		if err := json.Unmarshal([]byte(showOutput), &payload); err != nil {
			t.Fatalf("decode show shape: %v output=%q", err, showOutput)
		}
		if _, exists := payload.Goal["suspended"]; exists {
			t.Fatalf("goal show JSON unexpectedly contains runtime-local suspension: %q", showOutput)
		}
	}

	assertGoalCommandSubprocessShow(record.Meta.Goal)

	dormantOutput, dormantErr, dormantRunErr := runGoalCommandSubprocessRaw(t, kentPath, unboundWorktree, store.Meta().SessionID, "set", "replacement dormant goal CLI")
	if dormantRunErr == nil {
		t.Fatal("dormant goal mutation unexpectedly succeeded")
	}
	if dormantOutput != "" {
		t.Fatalf("dormant goal mutation stdout = %q, want empty", dormantOutput)
	}
	if got := nonEmptyLineCount(t, dormantErr); got != 1 {
		t.Fatalf("dormant goal mutation stderr lines = %d, want 1", got)
	}
	record, err = metadataStore.ResolvePersistedSession(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession after dormant mutation: %v", err)
	}
	if goal := record.Meta.Goal; goal == nil || goal.Objective != "exercise live goal CLI" || goal.Status != session.GoalStatusActive {
		t.Fatalf("persisted goal after dormant mutation = %+v", goal)
	}

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
	assertGoalCommandSubprocessShow(record.Meta.Goal)

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

	shellRequestID := runtimeids.NewRuntimeClientRequestID()
	if err := remote.SubmitUserShellCommand(context.Background(), serverapi.RuntimeSubmitUserShellCommandRequest{
		ClientRequestID: shellRequestID.String(),
		SessionID:       store.Meta().SessionID,
		Command:         shellQuote(kentPath) + " goal complete --confirm",
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindUserShell,
			ClientRequestID: shellRequestID,
		},
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
	if _, err := remote.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-goal-cli-e2e",
		SessionID:       store.Meta().SessionID,
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	assertGoalCommandSubprocessShow(record.Meta.Goal)
}

func nonEmptyLineCount(t *testing.T, text string) int {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(text))
	count := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan process output lines: %v", err)
	}
	return count
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
