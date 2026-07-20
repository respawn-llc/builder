package app

import (
	"context"
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

func TestPrepareSessionUIRunCarriesCapturedClientHookPlan(t *testing.T) {
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
	server := &testEmbeddedServer{
		cfg: config.App{PersistenceRoot: t.TempDir()},
		sessionLifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, nil
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return &runtimeLaunchPlan{}, nil
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
	if request.hookAttachmentPlan != hookPlan {
		t.Fatalf("prepared hook attachment plan = %+v, want captured plan %+v", request.hookAttachmentPlan, hookPlan)
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
