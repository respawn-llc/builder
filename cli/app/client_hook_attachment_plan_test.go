package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestClientHookAttachmentPlanIsRootlessAndIndependentOfServerLocality(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	taskID, err := lifecyclecontract.ParseWorkflowTaskID("dynamic-task")
	if err != nil {
		t.Fatalf("parse workflow task id: %v", err)
	}
	title := "dynamic session title"
	command := []string{"dynamic-hook", "--fixed"}
	settings := loadClientSettingsWithLifecycleCommand(t, command)
	intent := serverapi.OpenExistingSessionLaunchIntent(sessionID)
	base := sessionLaunchPlan{
		Mode:           launchModeInteractive,
		SessionID:      sessionID.String(),
		SessionName:    title,
		WorkflowTaskID: &taskID,
	}

	local, err := deriveClientHookAttachmentPlan(settings, intent, base)
	if err != nil {
		t.Fatalf("derive local hook attachment plan: %v", err)
	}
	remotePlan := base
	remotePlan.ExecutionTarget = clientui.SessionExecutionTarget{
		WorkspaceRoot:    "/server/remote-only",
		EffectiveWorkdir: "/server/remote-only/subdir",
	}
	remotePlan.StatusConfig.OwnsServer = false
	remote, err := deriveClientHookAttachmentPlan(settings, intent, remotePlan)
	if err != nil {
		t.Fatalf("derive remote hook attachment plan: %v", err)
	}
	if !reflect.DeepEqual(local, remote) {
		t.Fatalf("local and remote hook plans differ:\nlocal=%+v\nremote=%+v", local, remote)
	}
	command[0] = "mutated"
	if got := local.Argv(); !reflect.DeepEqual(got, []string{"dynamic-hook", "--fixed"}) {
		t.Fatalf("captured argv = %q, want immutable copied command", got)
	}
	returned := local.Argv()
	returned[0] = "mutated accessor"
	if got := local.Argv(); !reflect.DeepEqual(got, []string{"dynamic-hook", "--fixed"}) {
		t.Fatalf("mutating returned argv changed captured plan: %q", got)
	}
	if local.OpeningKind() != lifecyclecontract.OpeningKindResumed ||
		local.SessionID() != sessionID ||
		local.SessionTitle() == nil || *local.SessionTitle() != title ||
		local.WorkflowTaskID() == nil || local.WorkflowTaskID().String() != taskID.String() {
		t.Fatalf("rootless hook attachment plan = %+v", local)
	}
	returnedTitle := local.SessionTitle()
	*returnedTitle = "mutated title accessor"
	if got := local.SessionTitle(); got == nil || *got != title {
		t.Fatalf("mutating returned title changed captured plan: %v", got)
	}

	planType := reflect.TypeOf(clientHookAttachmentPlan{})
	allowedFields := map[string]struct{}{
		"argv":           {},
		"openingKind":    {},
		"sessionID":      {},
		"sessionTitle":   {},
		"workflowTaskID": {},
	}
	for index := 0; index < planType.NumField(); index++ {
		field := planType.Field(index).Name
		if _, allowed := allowedFields[field]; !allowed {
			t.Fatalf("hook attachment plan exposes unexpected authority %q", field)
		}
	}
	if planType.NumField() != len(allowedFields) {
		t.Fatalf("hook attachment plan field count = %d, want only rootless client context fields", planType.NumField())
	}
}

func TestClientHookAttachmentPlanIsAbsentWithoutConfigOrOutsideInteractiveTUI(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	create := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	plan := sessionLaunchPlan{
		Mode:      launchModeInteractive,
		SessionID: sessionID.String(),
	}

	absent, err := deriveClientHookAttachmentPlan(config.ClientSettings{}, create, plan)
	if err != nil {
		t.Fatalf("derive absent hook attachment plan: %v", err)
	}
	if absent != nil {
		t.Fatalf("absent command produced hook attachment plan %+v", absent)
	}

	configured, err := deriveClientHookAttachmentPlan(
		loadClientSettingsWithLifecycleCommand(t, []string{"dynamic-hook"}),
		create,
		plan,
	)
	if err != nil {
		t.Fatalf("derive configured new hook attachment plan: %v", err)
	}
	if configured == nil || configured.OpeningKind() != lifecyclecontract.OpeningKindNew {
		t.Fatalf("new opening hook attachment plan = %+v, want opening kind new", configured)
	}

	plan.Mode = launchModeHeadless
	headless, err := deriveClientHookAttachmentPlan(
		loadClientSettingsWithLifecycleCommand(t, []string{"dynamic-hook"}),
		create,
		plan,
	)
	if err != nil {
		t.Fatalf("derive headless hook attachment plan: %v", err)
	}
	if headless != nil {
		t.Fatalf("headless launch received hook attachment plan %+v", headless)
	}
}

func TestPrepareSessionUIRunOpensConfiguredAttachmentThroughInitialReducerEvent(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "records.jsonl")
	t.Setenv(lifecycleHookHelperEnvironmentName, "1")
	sessionID := runtimeids.NewSessionID()
	settings := loadClientSettingsWithLifecycleCommand(t, []string{
		os.Args[0],
		"-test.run=^TestLifecycleHookDispatcherHelper$",
		"--",
		recordPath,
		"session-start",
	})
	intent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	plan := sessionLaunchPlan{
		Mode:        launchModeInteractive,
		SessionID:   sessionID.String(),
		SessionName: "opened session",
	}
	hookPlan, err := deriveClientHookAttachmentPlan(settings, intent, plan)
	if err != nil {
		t.Fatalf("derive hook attachment plan: %v", err)
	}
	server := &testEmbeddedServer{
		cfg: config.App{PersistenceRoot: t.TempDir()},
		sessionLifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, nil
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return &runtimeLaunchPlan{Wiring: &runtimeWiring{
				eventDispatcher: newUIEventDispatcher(make(chan ongoingTranscriptEvent)),
				terminalFocus:   newTerminalFocusState(),
			}}, nil
		},
	}

	runtimePlan, request, err := prepareSessionUIRun(
		context.Background(),
		server,
		newSessionLaunchPlanner(server),
		plan,
		"",
		false,
		"",
		false,
		hookPlan,
	)
	if err != nil {
		t.Fatalf("prepare session UI run: %v", err)
	}
	defer func() { _ = runtimePlan.Close() }()
	if runtimePlan.lifecycleHookDispatcher == nil ||
		request.wiring.lifecycleCoordinator == nil ||
		request.wiring.eventDispatcher == nil {
		t.Fatalf("prepared lifecycle attachment = runtime %+v wiring %+v", runtimePlan, request.wiring)
	}

	model := newProjectedStaticUIModel(
		WithUIEventDispatcher(request.wiring.eventDispatcher),
		WithUIClientLifecycleCoordinator(request.wiring.lifecycleCoordinator),
	)
	message := request.wiring.eventDispatcher.wait()()
	next, _ := model.Update(message)
	model = next.(*uiModel)

	records := waitForLifecycleHookHelperRecords(t, recordPath, 1)
	var payload struct {
		Category lifecyclecontract.Category `json:"category"`
		Context  struct {
			SessionID    string `json:"session_id"`
			SessionTitle string `json:"session_title"`
		} `json:"context"`
		Details struct {
			Kind lifecyclecontract.OpeningKind `json:"kind"`
		} `json:"details"`
	}
	if err := json.Unmarshal(records[0].Payload, &payload); err != nil {
		t.Fatalf("decode session start payload: %v", err)
	}
	if payload.Category != lifecyclecontract.CategorySessionStart ||
		payload.Context.SessionID != sessionID.String() ||
		payload.Context.SessionTitle != plan.SessionName ||
		payload.Details.Kind != lifecyclecontract.OpeningKindNew {
		t.Fatalf("session start payload = %+v", payload)
	}

	request.wiring.lifecycleCoordinator.AcceptSessionIdentity(clientui.TranscriptSessionIdentity{
		SessionID: sessionID,
	})
	if err := runtimePlan.Close(); err != nil {
		t.Fatalf("close prepared lifecycle attachment: %v", err)
	}
	if records = waitForLifecycleHookHelperRecords(t, recordPath, 1); len(records) != 1 {
		t.Fatalf("identity hydration produced %d session start records, want one", len(records))
	}
}

func TestPrepareSessionUIRunDoesNotOpenHookBeforePreparationSucceeds(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	settings := loadClientSettingsWithLifecycleCommand(t, []string{"dynamic-hook"})
	intent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	plan := sessionLaunchPlan{
		Mode:      launchModeInteractive,
		SessionID: sessionID.String(),
	}
	hookPlan, err := deriveClientHookAttachmentPlan(settings, intent, plan)
	if err != nil {
		t.Fatalf("derive hook attachment plan: %v", err)
	}
	prepared := &runtimeLaunchPlan{Wiring: &runtimeWiring{
		eventDispatcher: newUIEventDispatcher(make(chan ongoingTranscriptEvent)),
		terminalFocus:   newTerminalFocusState(),
	}}
	preparationErr := io.ErrUnexpectedEOF
	server := &testEmbeddedServer{
		cfg: config.App{PersistenceRoot: t.TempDir()},
		sessionLifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, preparationErr
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return prepared, nil
		},
	}

	runtimePlan, _, err := prepareSessionUIRun(
		context.Background(),
		server,
		newSessionLaunchPlanner(server),
		plan,
		"",
		false,
		"",
		false,
		hookPlan,
	)
	if !errors.Is(err, preparationErr) {
		t.Fatalf("prepare session UI run error = %v, want %v", err, preparationErr)
	}
	if runtimePlan != nil {
		t.Fatalf("failed preparation returned runtime plan %+v", runtimePlan)
	}
	if prepared.lifecycleHookDispatcher != nil ||
		prepared.Wiring.lifecycleCoordinator != nil ||
		prepared.Wiring.eventDispatcher.hasInitialClientHookAttachment() {
		t.Fatalf("failed preparation opened lifecycle attachment: runtime=%+v wiring=%+v", prepared, prepared.Wiring)
	}
}

func TestPrepareSessionUIRunWithoutHookConfigLeavesLifecycleAttachmentAbsent(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	plan := sessionLaunchPlan{
		Mode:      launchModeInteractive,
		SessionID: sessionID.String(),
	}
	server := &testEmbeddedServer{
		cfg: config.App{PersistenceRoot: t.TempDir()},
		sessionLifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, nil
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return &runtimeLaunchPlan{Wiring: &runtimeWiring{
				eventDispatcher: newUIEventDispatcher(make(chan ongoingTranscriptEvent)),
				terminalFocus:   newTerminalFocusState(),
			}}, nil
		},
	}

	runtimePlan, request, err := prepareSessionUIRun(
		context.Background(),
		server,
		newSessionLaunchPlanner(server),
		plan,
		"",
		false,
		"",
		false,
		nil,
	)
	if err != nil {
		t.Fatalf("prepare session UI run without hook config: %v", err)
	}
	defer func() { _ = runtimePlan.Close() }()
	if runtimePlan.lifecycleHookDispatcher != nil ||
		request.wiring.lifecycleCoordinator != nil ||
		request.wiring.eventDispatcher.hasInitialClientHookAttachment() {
		t.Fatalf("absent hook config opened lifecycle attachment: runtime=%+v wiring=%+v", runtimePlan, request.wiring)
	}
}

func loadClientSettingsWithLifecycleCommand(t *testing.T, command []string) config.ClientSettings {
	t.Helper()
	configRoot := t.TempDir()
	workspace := t.TempDir()
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, strconv.Quote(arg))
	}
	contents := "[hooks.client]\nlifecycle = [" + strings.Join(quoted, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write client config: %v", err)
	}
	_, settings, err := config.LoadInteractive(workspace, config.LoadOptions{ConfigRoot: configRoot})
	if err != nil {
		t.Fatalf("load interactive client config: %v", err)
	}
	return settings
}
