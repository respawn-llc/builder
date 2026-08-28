package worktree

import (
	"context"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func sessionTargetWorktreeID(target clientui.SessionExecutionTarget) string {
	if target.Worktree == nil {
		return ""
	}
	return strings.TrimSpace(target.Worktree.ID)
}

func contractSessionTargetWorktreeID(target *worktreepb.SessionExecutionTarget) string {
	if target == nil || target.Worktree == nil {
		return ""
	}
	return strings.TrimSpace(target.Worktree.Id)
}

func createWorktreeRequestForTest(
	setupOperationID worktreecontract.SetupOperationID,
	sessionID string,
	rootPath string,
	baseRef string,
	createBranch bool,
	branchName string,
) *worktreepb.CreateRequest {
	return &worktreepb.CreateRequest{
		SetupOperationId: setupOperationID.String(),
		SessionId:        sessionID,
		Spec: &worktreepb.CreateSpec{
			BaseRef:      nonblankPointer(baseRef),
			CreateBranch: createBranch,
			BranchName:   nonblankPointer(branchName),
		},
		RootPath: nonblankPointer(rootPath),
	}
}

type serviceTestPublisher struct {
	mu          sync.Mutex
	identityErr error
	outcomes    []clientui.WorktreeTransitionOutcome
	ready       chan struct{}
}

func (p *serviceTestPublisher) PublishSessionIdentity(string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identityErr
}

func (p *serviceTestPublisher) PublishWorktreeTransitionOutcome(_ string, outcome clientui.WorktreeTransitionOutcome) {
	p.mu.Lock()
	p.outcomes = append(p.outcomes, outcome)
	if p.ready == nil {
		p.ready = make(chan struct{}, 1)
	}
	ready := p.ready
	p.mu.Unlock()
	select {
	case ready <- struct{}{}:
	default:
	}
}

type serviceTestProcessSource struct {
	snapshots []shelltool.Snapshot
}

func (s *serviceTestProcessSource) CurrentSnapshots() []shelltool.Snapshot {
	return append([]shelltool.Snapshot(nil), s.snapshots...)
}

type serviceTestEnv struct {
	t             *testing.T
	ctx           context.Context
	store         *metadata.Store
	cfg           config.App
	binding       metadata.Binding
	session       *session.Store
	authority     *sessionruntime.Authority
	publisher     *serviceTestPublisher
	processes     *serviceTestProcessSource
	service       *Service
	leaseID       string
	workspaceRoot string
	baseDir       string
}

func TestCreateWorktreeMarksProvenanceAndRunsSetupScriptWithProjectID(t *testing.T) {
	env := newServiceTestEnv(t)
	payloadPath := filepath.Join(t.TempDir(), "worktree-payload.json")
	stdinPath := filepath.Join(t.TempDir(), "worktree-stdin.json")
	argsPath := filepath.Join(t.TempDir(), "worktree-args.txt")
	cwdPath := filepath.Join(t.TempDir(), "worktree-cwd.txt")
	scriptRelpath := filepath.Join("scripts", "setup-worktree.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\npwd > %q\nprintf '%%s\n%%s\n%%s\n' \"$1\" \"$2\" \"$3\" > %q\ncat > %q\nprintf '%%s' \"$KENT_WORKTREE_PAYLOAD_JSON\" > %q\n", cwdPath, argsPath, stdinPath, payloadPath))
	env.service.setupScript = scriptRelpath

	resp, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		"",
		"HEAD",
		true,
		"feature/create-provenance",
	))
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	createdView := worktreeViewFromListEntryForTest(resp.Worktree)
	if !createdView.CreatedBranch {
		t.Fatal("expected create to report created branch")
	}
	if !createdView.Managed {
		t.Fatal("expected worktree managed=true")
	}
	if contractSessionTargetWorktreeID(resp.Target) != "" {
		t.Fatalf("create changed session target to %q", contractSessionTargetWorktreeID(resp.Target))
	}
	if resp.Target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("create effective workdir = %q, want %q", resp.Target.EffectiveWorkdir, env.workspaceRoot)
	}
	if !createdView.CreatedBranch {
		t.Fatal("expected worktree created_branch=true")
	}
	if createdView.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("origin session id = %q, want %q", createdView.OriginSessionID, env.session.Meta().SessionID)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, createdView.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if !record.Managed || !record.CreatedBranch || record.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("unexpected worktree record: %+v", record)
	}
	payload := waitForSetupPayload(t, payloadPath)
	if payload.ProjectID != env.binding.ProjectID {
		t.Fatalf("setup payload project_id = %q, want %q", payload.ProjectID, env.binding.ProjectID)
	}
	if payload.WorkspaceID != env.binding.WorkspaceID {
		t.Fatalf("setup payload workspace_id = %q, want %q", payload.WorkspaceID, env.binding.WorkspaceID)
	}
	if payload.SessionID == nil || *payload.SessionID != env.session.Meta().SessionID {
		t.Fatalf("setup payload session_id = %v, want %q", payload.SessionID, env.session.Meta().SessionID)
	}
	if payload.WorktreeID != createdView.WorktreeID {
		t.Fatalf("setup payload worktree_id = %q, want %q", payload.WorktreeID, createdView.WorktreeID)
	}
	if !payload.CreatedBranch {
		t.Fatal("expected setup payload created_branch=true")
	}
	if got := waitForFileText(t, cwdPath); got != createdView.CanonicalRoot {
		t.Fatalf("setup cwd = %q, want %q", got, createdView.CanonicalRoot)
	}
	if got := waitForFileLines(t, argsPath); len(got) != 3 || got[0] != env.workspaceRoot || got[1] != "feature/create-provenance" || got[2] != createdView.CanonicalRoot {
		t.Fatalf("setup args = %+v, want [%q %q %q]", got, env.workspaceRoot, "feature/create-provenance", createdView.CanonicalRoot)
	}
	if stdinPayload := waitForSetupPayload(t, stdinPath); !reflect.DeepEqual(stdinPayload, payload) {
		t.Fatalf("stdin payload = %+v, want %+v", stdinPayload, payload)
	}
	worktrees := mustListWorktrees(t, env)
	created := findWorktreeByID(t, worktrees.Worktrees, createdView.WorktreeID)
	if !created.Managed || !created.CreatedBranch || created.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("sync lost worktree provenance: %+v", created)
	}
}

func TestCreateWorktreeUsesCompactAutomaticRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	resp, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		"",
		"HEAD",
		true,
		"feature/compact-root",
	))
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	root := resp.Worktree.Topology.GetRegistered().Kent.CanonicalRoot
	canonicalBase, err := config.CanonicalWorkspaceRoot(env.baseDir)
	if err != nil {
		t.Fatalf("canonical base: %v", err)
	}
	expectedParent := filepath.Join(canonicalBase, normalizeWorkspacePathKey(filepath.Base(env.workspaceRoot)))
	if filepath.Dir(root) != expectedParent {
		t.Fatalf("compact root parent = %q, want normalized parent %q", filepath.Dir(root), expectedParent)
	}
	leaf := filepath.Base(root)
	if len(leaf) != 3 {
		t.Fatalf("regular compact leaf = %q, want three digits", leaf)
	}
	for _, r := range leaf {
		if r < '0' || r > '9' {
			t.Fatalf("regular compact leaf = %q, want digits", leaf)
		}
	}
}

func TestCreateWorktreePreservesExplicitRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	root := filepath.Join(env.baseDir, "explicit-root")
	resp, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		root,
		"HEAD",
		true,
		"feature/explicit-root",
	))
	if err != nil {
		t.Fatalf("CreateWorktree explicit: %v", err)
	}
	if got := resp.Worktree.Topology.GetRegistered().Kent.CanonicalRoot; got != root {
		canonical, canonicalErr := config.CanonicalWorkspaceRoot(root)
		if canonicalErr != nil || got != canonical {
			t.Fatalf("explicit root = %q, want %q", got, root)
		}
	}
}

func TestCreateWorktreeRejectsExplicitRootThroughBaseSymlinkBeforeGit(t *testing.T) {
	env := newServiceTestEnv(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(env.baseDir, "link")); err != nil {
		t.Fatalf("symlink base child: %v", err)
	}
	before := len(mustListWorktrees(t, env).Worktrees)
	_, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		filepath.Join("link", "new"),
		"HEAD",
		true,
		"feature/explicit-symlink-escape",
	))
	if err == nil {
		t.Fatal("CreateWorktree accepted a relative root that escaped through a base symlink")
	}
	if after := len(mustListWorktrees(t, env).Worktrees); after != before {
		t.Fatalf("failed explicit symlink creation changed Worktree count from %d to %d", before, after)
	}
}

func TestCreateWorktreeRejectsExplicitRootOverlappingSourceWorkspace(t *testing.T) {
	env := newServiceTestEnv(t)
	before := len(mustListWorktrees(t, env).Worktrees)
	_, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		filepath.Join(env.workspaceRoot, "nested"),
		"HEAD",
		true,
		"feature/explicit-source-overlap",
	))
	if err == nil {
		t.Fatal("CreateWorktree accepted an explicit root overlapping the source Workspace")
	}
	if after := len(mustListWorktrees(t, env).Worktrees); after != before {
		t.Fatalf("failed overlapping explicit creation changed Worktree count from %d to %d", before, after)
	}
}

func TestCreateWorktreeRejectsExplicitRootNestedInExistingManagedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	existingRoot := filepath.Join(env.baseDir, "existing-root")
	if _, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		existingRoot,
		"HEAD",
		true,
		"feature/existing-managed-root",
	)); err != nil {
		t.Fatalf("CreateWorktree existing root: %v", err)
	}
	before := len(mustListWorktrees(t, env).Worktrees)

	_, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		filepath.Join(existingRoot, "nested"),
		"HEAD",
		true,
		"feature/nested-managed-root",
	))
	if err == nil {
		t.Fatal("CreateWorktree accepted an explicit root nested in an existing managed Worktree")
	}
	if after := len(mustListWorktrees(t, env).Worktrees); after != before {
		t.Fatalf("failed nested creation changed Worktree count from %d to %d", before, after)
	}
}

func TestCreateWorktreeRejectsExplicitRootNestedInManagedWorktreeFromOtherProject(t *testing.T) {
	env := newServiceTestEnv(t)
	otherWorkspaceRoot := filepath.Join(env.baseDir, "other-workspace")
	if err := os.MkdirAll(otherWorkspaceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll other workspace: %v", err)
	}
	otherBinding, err := env.store.CreateProjectForWorkspace(env.ctx, otherWorkspaceRoot, "Other")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	otherManagedRoot := filepath.Join(env.baseDir, "other-managed-root")
	if err := os.MkdirAll(otherManagedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll other managed root: %v", err)
	}
	if err := env.store.UpsertWorktreeRecord(env.ctx, metadata.WorktreeRecord{
		ID:            uuid.NewString(),
		WorkspaceID:   otherBinding.WorkspaceID,
		CanonicalRoot: otherManagedRoot,
		Managed:       true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	before := len(mustListWorktrees(t, env).Worktrees)

	_, err = env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		filepath.Join(otherManagedRoot, "nested"),
		"HEAD",
		true,
		"feature/nested-other-project-managed-root",
	))
	if err == nil {
		t.Fatal("CreateWorktree accepted a root nested in another project's managed Worktree")
	}
	if after := len(mustListWorktrees(t, env).Worktrees); after != before {
		t.Fatalf("failed cross-project nested creation changed Worktree count from %d to %d", before, after)
	}
}

func TestCreateWorktreeBlocksUntilSetupCompletesBeforeSessionSwitch(t *testing.T) {
	env := newServiceTestEnv(t)
	startedPath := filepath.Join(t.TempDir(), "started")
	releasePath := filepath.Join(t.TempDir(), "release")
	markerPath := filepath.Join(t.TempDir(), "marker")
	scriptRelpath := filepath.Join("scripts", "blocking-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nprintf started > %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nprintf marker > %q\n", startedPath, releasePath, markerPath))
	env.service.setupScript = scriptRelpath
	setupID := worktreecontract.NewSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, &worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	type createResult struct {
		resp *worktreepb.CreateSuccess
		err  error
	}
	resultCh := make(chan createResult, 1)
	go func() {
		resp, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
			setupID,
			env.session.Meta().SessionID,
			"",
			"HEAD",
			true,
			"feature/create-blocking",
		))
		resultCh <- createResult{resp: resp, err: err}
	}()

	started := waitForFileText(t, startedPath)
	if started != "started" {
		t.Fatalf("started marker = %q, want started", started)
	}
	evt, err := sub.Next(env.ctx)
	if err != nil {
		t.Fatalf("setup event: %v", err)
	}
	if evt.GetStarted() == nil || evt.SetupOperationId != setupID.String() ||
		evt.GetStarted().ScriptPath == "" || evt.GetStarted().WorktreeRoot == "" {
		t.Fatalf("started setup event = %+v", evt)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("CreateWorktree returned before setup release: resp=%+v err=%v", result.resp, result.err)
	default:
	}
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("session target changed while setup blocked: %+v", target)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o644); err != nil {
		t.Fatalf("release setup: %v", err)
	}
	var result createResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for CreateWorktree")
	}
	if result.err != nil {
		t.Fatalf("CreateWorktree: %v", result.err)
	}
	if got := waitForFileText(t, markerPath); got != "marker" {
		t.Fatalf("setup marker = %q, want marker", got)
	}
	target, err = env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after: %v", err)
	}
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("session target after setup = %+v, want unchanged main target", target)
	}
}

func TestCreateWorktreeSetupFailureKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	scriptRelpath := filepath.Join("scripts", "fails.sh")
	longOutput := strings.Repeat("x", setupDiagnosticLimitBytes+128)
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q >&2\nexit 7\n", longOutput))
	env.service.setupScript = scriptRelpath
	setupID := worktreecontract.NewSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, &worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	_, err = env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		setupID,
		env.session.Meta().SessionID,
		"",
		"HEAD",
		true,
		"feature/setup-fails",
	))
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want setup failure")
	}
	var retained *worktreecontract.SetupRetainedError
	if !errors.As(err, &retained) || retained.Details.Worktree == nil {
		t.Fatalf("CreateWorktree error = %T %v, want retained worktree facts", err, err)
	}
	expectedRoot := retained.Details.Worktree.Kent.CanonicalRoot
	if _, statErr := os.Stat(expectedRoot); statErr != nil {
		t.Fatalf("expected setup-failed worktree kept, stat err=%v", statErr)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.GetFailed() == nil || evt.GetFailed().Cause.GetProcessExit() == nil ||
		evt.GetFailed().Cause.GetProcessExit().ExitCode != 7 {
		t.Fatalf("failure setup event = %+v", evt)
	}
	if evt.GetFailed().Cause.GetProcessExit().Stderr == nil || len(*evt.GetFailed().Cause.GetProcessExit().Stderr) > setupDiagnosticLimitBytes {
		t.Fatalf("stderr diagnostic = %v, want present and <= %d bytes", evt.GetFailed().Cause.GetProcessExit().Stderr, setupDiagnosticLimitBytes)
	}
}

func TestCreateWorktreeSetupTimeoutKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	scriptRelpath := filepath.Join("scripts", "timeout.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), "#!/bin/sh\nsleep 10\n")
	env.service.setupScript = scriptRelpath
	env.service.setupTimeoutSeconds = 1
	setupID := worktreecontract.NewSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, &worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	_, err = env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		setupID,
		env.session.Meta().SessionID,
		"",
		"HEAD",
		true,
		"feature/setup-timeout",
	))
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want setup timeout")
	}
	expectedScript, canonicalErr := config.CanonicalWorkspaceRoot(filepath.Join(env.workspaceRoot, scriptRelpath))
	if canonicalErr != nil {
		t.Fatalf("canonical expected script: %v", canonicalErr)
	}
	var setupErr *setupScriptError
	if !errors.As(err, &setupErr) {
		t.Fatalf("timeout error = %#v, want setupScriptError", err)
	}
	if !setupErr.Timeout || setupErr.TimeoutSeconds != 1 || setupErr.ScriptPath != expectedScript || setupErr.WorktreeRoot == "" {
		t.Fatalf("timeout setup error fields timeout=%t seconds=%d script=%q want=%q root=%q", setupErr.Timeout, setupErr.TimeoutSeconds, setupErr.ScriptPath, expectedScript, setupErr.WorktreeRoot)
	}
	if _, statErr := os.Stat(setupErr.WorktreeRoot); statErr != nil {
		t.Fatalf("expected timed-out setup worktree kept, stat err=%v", statErr)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.GetFailed() == nil || evt.GetFailed().Cause.GetTimeout() == nil {
		t.Fatalf("timeout setup event = %+v", evt)
	}
	if evt.GetFailed().Diagnostic != setupErr.Error() {
		t.Fatalf("timeout setup event = %+v, want final diagnostic", evt)
	}
}

func TestCreateWorktreeSetupCancellationKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	startedPath := filepath.Join(t.TempDir(), "started")
	scriptRelpath := filepath.Join("scripts", "cancel.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nready_path=%q\nready_tmp=\"${ready_path}.$$\"\nprintf started > \"$ready_tmp\"\nmv \"$ready_tmp\" \"$ready_path\"\nexec sleep 10\n", startedPath))
	env.service.setupScript = scriptRelpath
	setupID := worktreecontract.NewSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, &worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	ctx, cancel := context.WithCancel(env.ctx)
	resultCh := make(chan error, 1)
	go func() {
		_, err := env.service.CreateWorktree(ctx, createWorktreeRequestForTest(
			setupID,
			env.session.Meta().SessionID,
			"",
			"HEAD",
			true,
			"feature/setup-cancel",
		))
		resultCh <- err
	}()
	if got := waitForFileText(t, startedPath); got != "started" {
		t.Fatalf("started marker = %q, want started", got)
	}
	cancel()
	select {
	case err = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for canceled create")
	}
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want cancellation error")
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.GetFailed() == nil || evt.GetFailed().Cause.GetCanceled() == nil {
		t.Fatalf("canceled setup event = %+v", evt)
	}
	var retained *worktreecontract.SetupRetainedError
	if !errors.As(err, &retained) || retained.Details.Worktree == nil {
		t.Fatalf("canceled setup error = %T %v, want retained worktree", err, err)
	}
	if _, statErr := os.Stat(retained.Details.Worktree.Kent.CanonicalRoot); statErr != nil {
		t.Fatalf("expected canceled setup worktree kept, stat err=%v", statErr)
	}
}

func TestCreateWorktreeInvalidSetupScriptsKeepWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	for _, tc := range []struct {
		name       string
		scriptPath string
		branchName string
		prepare    func(*testing.T)
	}{
		{
			name:       "directory",
			scriptPath: filepath.Join("scripts", "directory"),
			branchName: "feature/setup-directory",
			prepare: func(t *testing.T) {
				if err := os.MkdirAll(filepath.Join(env.workspaceRoot, "scripts", "directory"), 0o755); err != nil {
					t.Fatalf("MkdirAll script dir: %v", err)
				}
			},
		},
		{
			name:       "missing",
			scriptPath: filepath.Join("scripts", "missing.sh"),
			branchName: "feature/setup-missing",
			prepare:    func(*testing.T) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare(t)
			env.service.setupScript = tc.scriptPath
			setupID := worktreecontract.NewSetupOperationID()
			sub, err := env.service.SubscribeWorktreeSetup(env.ctx, &worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
			if err != nil {
				t.Fatalf("SubscribeWorktreeSetup: %v", err)
			}
			defer func() { _ = sub.Close() }()
			_, err = env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
				setupID,
				env.session.Meta().SessionID,
				"",
				"HEAD",
				true,
				tc.branchName,
			))
			if err == nil {
				t.Fatal("CreateWorktree succeeded, want invalid setup script error")
			}
			assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
			evt := nextSetupTerminalEvent(t, sub)
			if evt.GetFailed() == nil || strings.TrimSpace(evt.GetFailed().Diagnostic) == "" {
				t.Fatalf("invalid setup event = %+v", evt)
			}
			var retained *worktreecontract.SetupRetainedError
			if !errors.As(err, &retained) || retained.Details.Worktree == nil {
				t.Fatalf("invalid setup error = %T %v, want retained worktree", err, err)
			}
			if _, statErr := os.Stat(retained.Details.Worktree.Kent.CanonicalRoot); statErr != nil {
				t.Fatalf("expected invalid-setup worktree kept, stat err=%v", statErr)
			}
		})
	}
}

func TestCreateWorktreeAllowsExistingRefWithoutCreatingBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/existing-ref")

	resp, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		"",
		"feature/existing-ref",
		false,
		"",
	))
	if err != nil {
		t.Fatalf("CreateWorktree existing ref: %v", err)
	}
	createdView := worktreeViewFromListEntryForTest(resp.Worktree)
	if createdView.CreatedBranch {
		t.Fatal("expected created_branch=false for existing ref")
	}
	if createdView.BranchName != "feature/existing-ref" {
		t.Fatalf("branch name = %q, want feature/existing-ref", createdView.BranchName)
	}
	if !createdView.Managed {
		t.Fatal("expected managed worktree for existing ref")
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, createdView.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if record.CreatedBranch {
		t.Fatalf("expected created_branch=false in metadata, got %+v", record)
	}
}

func TestCreateWorktreeFromCheckedOutHEADRollsBackDetachedRegistration(t *testing.T) {
	env := newServiceTestEnv(t)
	_, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		"",
		"HEAD",
		false,
		"",
	))
	if err == nil {
		t.Fatal("CreateWorktree from checked-out HEAD succeeded")
	}
	worktrees, listErr := env.service.git.List(env.ctx, env.workspaceRoot)
	if listErr != nil {
		t.Fatalf("list worktrees after detached create rollback: %v", listErr)
	}
	for _, worktree := range worktrees {
		if !worktree.IsMain {
			t.Fatalf("detached worktree remained registered after failed create: %+v", worktree)
		}
	}
	records, listErr := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if listErr != nil {
		t.Fatalf("list worktree records after detached create rollback: %v", listErr)
	}
	if len(records) != 0 {
		t.Fatalf("detached Kent worktree records remained after failed create: %+v", records)
	}
}

func TestListWorktreesDoesNotResetManagedProvenanceWhenRootIsReused(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/provenance-stale")

	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", created.CanonicalRoot)
	runGit(t, env.workspaceRoot, "worktree", "add", "--detach", created.CanonicalRoot, "HEAD")

	worktrees := mustListWorktrees(t, env).Worktrees
	for _, entry := range worktrees {
		worktree := worktreeViewFromListEntryForTest(entry)
		if strings.TrimSpace(worktree.CanonicalRoot) != strings.TrimSpace(created.CanonicalRoot) {
			continue
		}
		if !worktree.Managed || !worktree.CreatedBranch || strings.TrimSpace(worktree.OriginSessionID) == "" {
			t.Fatalf("list mutated managed provenance for reused root: %+v", worktree)
		}
		return
	}
	t.Fatalf("expected reused worktree root %q in %+v", created.CanonicalRoot, worktrees)
}

func TestResolveWorktreeCreateTargetClassifiesBranchDetachedRefAndNewBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/existing-ref")

	existing, err := env.service.ResolveWorktreeCreateTarget(env.ctx, &worktreepb.CreateTargetResolveRequest{SessionId: env.session.Meta().SessionID, Target: "feature/existing-ref"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget existing: %v", err)
	}
	if existing.Resolution.Kind != worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH {
		t.Fatalf("existing kind = %q, want existing_branch", existing.Resolution.Kind)
	}

	detached, err := env.service.ResolveWorktreeCreateTarget(env.ctx, &worktreepb.CreateTargetResolveRequest{SessionId: env.session.Meta().SessionID, Target: "HEAD"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget detached: %v", err)
	}
	if detached.Resolution.Kind != worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF {
		t.Fatalf("detached kind = %q, want detached_ref", detached.Resolution.Kind)
	}

	newBranch, err := env.service.ResolveWorktreeCreateTarget(env.ctx, &worktreepb.CreateTargetResolveRequest{SessionId: env.session.Meta().SessionID, Target: "feature/new-branch"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget new branch: %v", err)
	}
	if newBranch.Resolution.Kind != worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH {
		t.Fatalf("new branch kind = %q, want new_branch", newBranch.Resolution.Kind)
	}
}

func TestResolveRequestedWorktreeRootPreservesExplicitRelativeRoot(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "missing-base")
	service := &Service{managedRoots: newManagedRootAllocator(baseDir, nil)}
	sourceRoot := t.TempDir()
	resolvedRoot, err := service.managedRoots.resolveExplicitRoot("nested/explicit", sourceRoot)
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	canonicalBase, err := config.CanonicalWorkspaceRoot(baseDir)
	if err != nil {
		t.Fatalf("canonical base: %v", err)
	}
	want, err := config.ResolveExistingAncestorRealPath(filepath.Join(canonicalBase, "nested/explicit"))
	if err != nil {
		t.Fatalf("canonical expected root: %v", err)
	}
	if resolvedRoot != want {
		t.Fatalf("resolved root = %q, want %q", resolvedRoot, want)
	}
}

func TestSwitchWorktreeClampsCwdAndRecordsPendingReminder(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/switch-clamp")
	if err := os.MkdirAll(filepath.Join(created.CanonicalRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll pkg: %v", err)
	}
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, "pkg")
	mainGit, found, err := env.service.git.FindCreatedWorktree(env.ctx, env.workspaceRoot, env.workspaceRoot)
	if err != nil || !found {
		t.Fatalf("find main worktree: found=%v err=%v", found, err)
	}
	previousRecord, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	previous := syncedWorktree{record: previousRecord, git: GitWorktree{Root: created.CanonicalRoot, Branch: mustLocalBranch(t, created.BranchName)}}
	respTarget, err := env.service.switchSessionTarget(env.ctx, sessionWorkspaceContext{
		target:        mustResolveServiceTestTarget(t, env),
		projectID:     env.binding.ProjectID,
		workspaceID:   env.binding.WorkspaceID,
		workspaceRoot: env.workspaceRoot,
		sessionID:     env.session.Meta().SessionID,
	}, &previous, syncedWorktree{
		record: metadata.WorktreeRecord{WorkspaceID: env.binding.WorkspaceID, CanonicalRoot: env.workspaceRoot},
		git:    mainGit,
	})
	if err != nil {
		t.Fatalf("switchSessionTarget: %v", err)
	}
	if sessionTargetWorktreeID(respTarget) != "" {
		t.Fatalf("target worktree id = %q, want main workspace", sessionTargetWorktreeID(respTarget))
	}
	if respTarget.CwdRelpath != "." {
		t.Fatalf("target cwd_relpath = %q, want .", respTarget.CwdRelpath)
	}
	if respTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("effective workdir = %q, want %q", respTarget.EffectiveWorkdir, env.workspaceRoot)
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(finalTarget) != "" || finalTarget.CwdRelpath != "." {
		t.Fatalf("unexpected final target after switch: %+v", finalTarget)
	}
	reopened, err := session.OpenByID(env.cfg.PersistenceRoot, env.session.Meta().SessionID, env.store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if reminder := reopened.Meta().WorktreeReminder; reminder == nil || reminder.EffectiveCwd != env.workspaceRoot {
		t.Fatalf("pending worktree reminder = %+v, want effective cwd %q", reminder, env.workspaceRoot)
	}
}

func TestListWorktreesReportsMissingCurrentWorktreeWithoutRetargeting(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/missing-current")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", created.CanonicalRoot)

	resp, err := env.service.ListWorktrees(env.ctx, &worktreepb.ListRequest{SessionId: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if contractSessionTargetWorktreeID(resp.Target) != created.WorktreeID {
		t.Fatalf("response target worktree id = %q, want %q", contractSessionTargetWorktreeID(resp.Target), created.WorktreeID)
	}
	foundMissing := false
	for _, entry := range resp.Worktrees {
		if worktreeIDFromListEntry(entry) == created.WorktreeID {
			foundMissing = entry.Topology.GetMissing() != nil && entry.Projection.IsCurrent
		}
	}
	if !foundMissing {
		t.Fatalf("missing current worktree not projected: %+v", resp.Worktrees)
	}
	resolved, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(resolved) != created.WorktreeID {
		t.Fatalf("stored target worktree id = %q, want %q", sessionTargetWorktreeID(resolved), created.WorktreeID)
	}
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if sessionTargetWorktreeID(otherTarget) != created.WorktreeID {
		t.Fatalf("other session was retargeted during list: %+v", otherTarget)
	}
}

func TestSwitchSessionTargetRejectsInvalidPreviousTargetBeforeMetadataMutation(t *testing.T) {
	env := newServiceTestEnv(t)
	invalidPrevious := clientui.SessionExecutionTarget{
		WorkspaceID:      env.binding.WorkspaceID,
		WorkspaceRoot:    env.workspaceRoot,
		Worktree:         &clientui.SessionExecutionWorktreeTarget{},
		CwdRelpath:       ".",
		EffectiveWorkdir: env.workspaceRoot,
	}
	workspaceCtx := sessionWorkspaceContext{
		sessionID:     env.session.Meta().SessionID,
		workspaceID:   env.binding.WorkspaceID,
		workspaceRoot: env.workspaceRoot,
		target:        invalidPrevious,
		projectID:     env.binding.ProjectID,
	}
	next := syncedWorktree{
		record: metadata.WorktreeRecord{
			ID:            "worktree-next",
			WorkspaceID:   env.binding.WorkspaceID,
			CanonicalRoot: filepath.Join(env.workspaceRoot, "next"),
		},
		git: GitWorktree{},
	}

	if _, err := env.service.switchSessionTarget(env.ctx, workspaceCtx, nil, next); err == nil {
		t.Fatal("expected switchSessionTarget to reject previous target with present empty worktree id")
	}
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("execution target mutated despite invalid previous target: %+v", target)
	}
}

func TestCreateWorktreeKeepsCreatedStateWhenPostSetupSwitchFails(t *testing.T) {
	env := newServiceTestEnv(t)
	resp, err := env.service.CreateWorktree(env.ctx, createWorktreeRequestForTest(
		worktreecontract.NewSetupOperationID(),
		env.session.Meta().SessionID,
		"",
		"HEAD",
		true,
		"feature/create-rollback",
	))
	if err != nil {
		t.Fatalf("CreateWorktree error = %v, want successful create without switching", err)
	}
	if contractSessionTargetWorktreeID(resp.Target) != "" {
		t.Fatalf("create changed target despite no-enter contract: target=%+v", resp.Target)
	}
	expectedRoot := resp.Worktree.Topology.GetRegistered().Kent.CanonicalRoot
	if got := runGit(t, env.workspaceRoot, "branch", "--list", "feature/create-rollback"); strings.Contains(got, "feature/create-rollback") {
		// Branch remains because setup has completed and the worktree is inspectable.
	} else {
		t.Fatalf("expected created branch kept after post-setup switch failure, got %q", got)
	}
	records, err := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("ListWorktreeRecordsByWorkspaceID: %v", err)
	}
	recordKept := false
	for _, record := range records {
		if strings.TrimSpace(record.CanonicalRoot) == strings.TrimSpace(expectedRoot) {
			recordKept = true
		}
	}
	if !recordKept {
		t.Fatalf("expected failed create worktree record kept for root %q", expectedRoot)
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(finalTarget) != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected session target unchanged after failed create, got %+v", finalTarget)
	}
}
