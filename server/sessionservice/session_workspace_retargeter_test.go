package sessionservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/registry"
	"core/server/session"
	"core/server/session/sessiontest"
	sessionruntime "core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type retargetMetadataStub struct {
	plan        metadata.SessionWorkspaceRetargetPlan
	result      metadata.SessionWorkspaceRetargetResult
	commitErr   error
	commitCalls int
}

func (s *retargetMetadataStub) PlanSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetPlan, error) {
	return s.plan, nil
}

func (s *retargetMetadataStub) CommitSessionWorkspaceRetarget(_ context.Context, _ metadata.SessionWorkspaceRetargetPlan, updatedAt time.Time) (metadata.SessionWorkspaceRetargetResult, error) {
	s.commitCalls++
	s.result.UpdatedAt = updatedAt
	return s.result, s.commitErr
}

type retargetRuntimeStub struct {
	blocked      bool
	released     bool
	published    bool
	onPublish    func()
	previousRoot string
	rebinds      []string
	store        *session.Store
}

func (s *retargetRuntimeStub) BlockSessionRuns([]string) func() {
	s.blocked = true
	return func() { s.released = true }
}

func (s *retargetRuntimeStub) PublishSessionIdentity(string, *clientui.SessionExecutionTarget) {
	s.published = true
	if s.onPublish != nil {
		s.onPublish()
	}
}

func (s *retargetRuntimeStub) RunSessionMaintenance(
	ctx context.Context,
	_ string,
	fn func(context.Context, *session.Store, *sessionruntime.ActiveRuntimeMaintenance) error,
) error {
	return fn(ctx, s.store, &sessionruntime.ActiveRuntimeMaintenance{
		PreviousWorkdir: s.previousRoot,
		Rebind: func(root string) error {
			s.rebinds = append(s.rebinds, root)
			return nil
		},
	})
}

type retargetProcessSource []shelltool.Snapshot

func (s retargetProcessSource) List() []shelltool.Snapshot {
	return append([]shelltool.Snapshot(nil), s...)
}

type blockingSessionMetadataObserver struct {
	store   *metadata.Store
	mu      sync.Mutex
	block   bool
	started chan struct{}
	release chan struct{}
}

func newBlockingSessionMetadataObserver(store *metadata.Store) *blockingSessionMetadataObserver {
	return &blockingSessionMetadataObserver{
		store:   store,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (o *blockingSessionMetadataObserver) Arm() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.block = true
}

func (o *blockingSessionMetadataObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	o.mu.Lock()
	block := o.block
	o.block = false
	o.mu.Unlock()
	if block {
		close(o.started)
		select {
		case <-o.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return o.store.ImportSessionSnapshot(ctx, snapshot)
}

type realSessionRetargetFixture struct {
	metadata            *metadata.Store
	sourceBinding       metadata.Binding
	targetProject       metadata.Binding
	parent              *session.Store
	child               *session.Store
	targetWorkspaceRoot string
	runtimes            *registry.RuntimeRegistry
	stores              *registry.SessionStoreRegistry
	maintenance         *sessionruntime.Service
}

func newRealSessionRetargetFixture(t *testing.T) realSessionRetargetFixture {
	t.Helper()
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	sourceBinding, err := metadataStore.RegisterWorkspaceBinding(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding source: %v", err)
	}
	targetProject, err := metadataStore.CreateProjectForWorkspace(ctx, t.TempDir(), "Target")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace target: %v", err)
	}
	sourceContainer := filepath.Join(persistenceRoot, "projects", sourceBinding.ProjectID, "sessions")
	parent, err := session.Create(
		sourceContainer,
		sourceBinding.WorkspaceName,
		sourceBinding.CanonicalRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create parent: %v", err)
	}
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("parent EnsureDurable: %v", err)
	}
	child, err := session.Create(
		sourceContainer,
		sourceBinding.WorkspaceName,
		sourceBinding.CanonicalRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create child: %v", err)
	}
	if err := child.EnsureDurable(); err != nil {
		t.Fatalf("child EnsureDurable: %v", err)
	}
	if err := child.SetParentSessionID(parent.Meta().SessionID); err != nil {
		t.Fatalf("SetParentSessionID: %v", err)
	}
	stores := registry.NewSessionStoreRegistry()
	stores.RegisterStore(child)
	runtimes := registry.NewRuntimeRegistry()
	maintenance := sessionruntime.NewService(
		persistenceRoot,
		metadataStore,
		nil,
		nil,
		nil,
		nil,
		runtimes,
		stores,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	return realSessionRetargetFixture{
		metadata:            metadataStore,
		sourceBinding:       sourceBinding,
		targetProject:       targetProject,
		parent:              parent,
		child:               child,
		targetWorkspaceRoot: t.TempDir(),
		runtimes:            runtimes,
		stores:              stores,
		maintenance:         maintenance,
	}
}

func (f realSessionRetargetFixture) retargeter(metadataSource sessionRetargetMetadata) *SessionWorkspaceRetargeter {
	return NewSessionWorkspaceRetargeter(metadataSource, f.runtimes, f.maintenance, retargetProcessSource{})
}

func projectContainsWorkspaceRoot(t *testing.T, store *metadata.Store, projectID string, workspaceRoot string) bool {
	t.Helper()
	workspaces, err := store.ListProjectWorkspaces(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListProjectWorkspaces: %v", err)
	}
	targetRoot := canonicalRetargetTestPath(t, workspaceRoot)
	for _, workspace := range workspaces {
		if canonicalRetargetTestPath(t, workspace.RootPath) == targetRoot {
			return true
		}
	}
	return false
}

func canonicalRetargetTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := config.CanonicalWorkspaceRoot(path)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot %q: %v", path, err)
	}
	return canonical
}

func newRetargetServiceFixture(t *testing.T) (*SessionWorkspaceRetargeter, *retargetMetadataStub, *retargetRuntimeStub, string, string) {
	t.Helper()
	sourceParent := t.TempDir()
	persistence := sessiontest.NewPersistence()
	sourceStore, err := session.Create(
		sourceParent,
		"source",
		t.TempDir(),
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	sourceDir := sourceStore.Dir()
	targetDir := filepath.Join(t.TempDir(), sourceStore.Meta().SessionID)
	targetRoot := t.TempDir()
	plan := metadata.SessionWorkspaceRetargetPlan{
		SessionID:             sourceStore.Meta().SessionID,
		SourceProject:         serverapi.ProjectReference{ID: "project-source", Name: "Source"},
		TargetProject:         serverapi.ProjectReference{ID: "project-target", Name: "Target"},
		TargetWorkspaceRoot:   targetRoot,
		SourceArtifactRelpath: "projects/project-source/sessions/" + sourceStore.Meta().SessionID,
		TargetArtifactRelpath: "projects/project-target/sessions/" + sourceStore.Meta().SessionID,
		SourceSessionDir:      sourceDir,
		TargetSessionDir:      targetDir,
	}
	metadataStub := &retargetMetadataStub{
		plan: plan,
		result: metadata.SessionWorkspaceRetargetResult{
			Binding: metadata.Binding{
				ProjectID:     "project-target",
				ProjectName:   "Target",
				WorkspaceID:   "workspace-target",
				CanonicalRoot: targetRoot,
				WorkspaceName: filepath.Base(targetRoot),
			},
			UpdatedAt: time.Now().UTC(),
		},
	}
	runtimeStub := &retargetRuntimeStub{store: sourceStore, previousRoot: sourceStore.Meta().WorkspaceRoot}
	service := NewSessionWorkspaceRetargeter(metadataStub, runtimeStub, runtimeStub, retargetProcessSource{})
	return service, metadataStub, runtimeStub, sourceDir, targetDir
}

func TestSessionWorkspaceRetargeterMovesArtifactThroughSessionMaintenance(t *testing.T) {
	service, metadataStub, runtimeStub, sourceDir, targetDir := newRetargetServiceFixture(t)
	runtimeStub.onPublish = func() {
		_ = runtimeStub.store.Meta()
	}
	type retargetOutcome struct {
		result metadata.SessionWorkspaceRetargetResult
		err    error
	}
	outcome := make(chan retargetOutcome, 1)
	go func() {
		result, err := service.RetargetWorkspace(context.Background(), metadata.SessionWorkspaceRetargetRequest{
			SessionID:     runtimeStub.store.Meta().SessionID,
			WorkspaceRoot: runtimeStub.store.Meta().WorkspaceRoot,
		})
		outcome <- retargetOutcome{result: result, err: err}
	}()
	var completed retargetOutcome
	select {
	case completed = <-outcome:
	case <-time.After(5 * time.Second):
		t.Fatal("RetargetWorkspace deadlocked while publishing active session identity")
	}
	if completed.err != nil {
		t.Fatalf("RetargetWorkspace: %v", completed.err)
	}
	if metadataStub.commitCalls != 1 || !runtimeStub.blocked || !runtimeStub.released {
		t.Fatalf("coordination = commit:%d blocked:%t released:%t", metadataStub.commitCalls, runtimeStub.blocked, runtimeStub.released)
	}
	if _, err := os.Stat(sourceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source artifact still exists: %v", err)
	}
	if info, err := os.Stat(targetDir); err != nil || !info.IsDir() {
		t.Fatalf("target artifact = %v, %v", info, err)
	}
	if runtimeStub.store.Dir() != targetDir {
		t.Fatalf("store dir = %q, want %q", runtimeStub.store.Dir(), targetDir)
	}
	if completed.result.Binding.ProjectID != "project-target" {
		t.Fatalf("result binding = %+v", completed.result.Binding)
	}
}

func TestSessionWorkspaceRetargeterRollsArtifactBackWhenMetadataCommitFails(t *testing.T) {
	service, metadataStub, runtimeStub, sourceDir, targetDir := newRetargetServiceFixture(t)
	metadataStub.commitErr = errors.New("commit failed")

	_, err := service.RetargetWorkspace(context.Background(), metadata.SessionWorkspaceRetargetRequest{
		SessionID:     runtimeStub.store.Meta().SessionID,
		WorkspaceRoot: runtimeStub.store.Meta().WorkspaceRoot,
	})
	if !errors.Is(err, metadataStub.commitErr) {
		t.Fatalf("RetargetWorkspace error = %v, want commit failure", err)
	}
	if info, statErr := os.Stat(sourceDir); statErr != nil || !info.IsDir() {
		t.Fatalf("source artifact was not restored: %v, %v", info, statErr)
	}
	if _, statErr := os.Stat(targetDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target artifact still exists: %v", statErr)
	}
	if runtimeStub.store.Dir() != sourceDir {
		t.Fatalf("store dir = %q, want unchanged %q", runtimeStub.store.Dir(), sourceDir)
	}
	if len(runtimeStub.rebinds) != 2 || runtimeStub.rebinds[1] != runtimeStub.previousRoot {
		t.Fatalf("runtime rebinds = %+v, want target then rollback", runtimeStub.rebinds)
	}
}

func TestSessionWorkspaceRetargeterRejectsOwnedBackgroundProcess(t *testing.T) {
	service, metadataStub, runtimeStub, sourceDir, targetDir := newRetargetServiceFixture(t)
	service.processes = retargetProcessSource{{
		ID:             "process-1",
		OwnerSessionID: runtimeStub.store.Meta().SessionID,
		Running:        true,
	}}

	_, err := service.RetargetWorkspace(context.Background(), metadata.SessionWorkspaceRetargetRequest{
		SessionID:     runtimeStub.store.Meta().SessionID,
		WorkspaceRoot: runtimeStub.store.Meta().WorkspaceRoot,
	})
	var retargetErr *serverapi.SessionRetargetError
	if !errors.As(err, &retargetErr) || retargetErr.Reason != serverapi.SessionRetargetBackgroundProcess {
		t.Fatalf("RetargetWorkspace error = %v, want background-process error", err)
	}
	if metadataStub.commitCalls != 0 || len(runtimeStub.rebinds) != 0 {
		t.Fatalf("failed rebind mutated coordination: commits=%d rebinds=%v", metadataStub.commitCalls, runtimeStub.rebinds)
	}
	if info, statErr := os.Stat(sourceDir); statErr != nil || !info.IsDir() {
		t.Fatalf("source artifact changed: %v, %v", info, statErr)
	}
	if _, statErr := os.Stat(targetDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target artifact exists: %v", statErr)
	}
}

func TestSessionWorkspaceRetargeterRejectsUnownedRunningProcessSnapshot(t *testing.T) {
	service, metadataStub, runtimeStub, sourceDir, targetDir := newRetargetServiceFixture(t)
	service.processes = retargetProcessSource{{
		ID:      "process-without-owner",
		Running: true,
	}}

	_, err := service.RetargetWorkspace(context.Background(), metadata.SessionWorkspaceRetargetRequest{
		SessionID:     runtimeStub.store.Meta().SessionID,
		WorkspaceRoot: runtimeStub.store.Meta().WorkspaceRoot,
	})
	if err == nil {
		t.Fatal("RetargetWorkspace accepted a running process without owner identity")
	}
	if metadataStub.commitCalls != 0 || len(runtimeStub.rebinds) != 0 {
		t.Fatalf("failed rebind mutated coordination: commits=%d rebinds=%v", metadataStub.commitCalls, runtimeStub.rebinds)
	}
	if info, statErr := os.Stat(sourceDir); statErr != nil || !info.IsDir() {
		t.Fatalf("source artifact changed: %v, %v", info, statErr)
	}
	if _, statErr := os.Stat(targetDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target artifact exists: %v", statErr)
	}
}

func TestSessionWorkspaceRetargeterMovesRealArtifactAndMetadataAcrossProjects(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	plan, err := fixture.metadata.PlanSessionWorkspaceRetarget(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	sourceDir := plan.SourceSessionDir

	result, err := fixture.retargeter(fixture.metadata).RetargetWorkspace(context.Background(), req)
	if err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}
	if !result.WorkspaceBindingCreated {
		t.Fatal("WorkspaceBindingCreated = false, want true")
	}
	if result.Binding.ProjectID != targetProjectID {
		t.Fatalf("target project = %q, want %q", result.Binding.ProjectID, targetProjectID)
	}
	if _, err := os.Stat(sourceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source artifact still exists: %v", err)
	}
	if info, err := os.Stat(plan.TargetSessionDir); err != nil || !info.IsDir() {
		t.Fatalf("target artifact = %v, %v", info, err)
	}
	record, err := fixture.metadata.ResolvePersistedSession(context.Background(), fixture.child.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if canonicalRetargetTestPath(t, record.SessionDir) != canonicalRetargetTestPath(t, plan.TargetSessionDir) {
		t.Fatalf("persisted session dir = %q, want %q", record.SessionDir, plan.TargetSessionDir)
	}
	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		fixture.child.Meta().SessionID,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != result.Binding.CanonicalRoot {
		t.Fatalf("reopened workspace root = %q, want %q", reopened.Meta().WorkspaceRoot, result.Binding.CanonicalRoot)
	}
	if reopened.Meta().ParentSessionID != fixture.parent.Meta().SessionID {
		t.Fatalf("reopened parent session = %q, want %q", reopened.Meta().ParentSessionID, fixture.parent.Meta().SessionID)
	}
	parentInSource, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.parent.Meta().SessionID, fixture.sourceBinding.ProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject parent: %v", err)
	}
	childInTarget, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, targetProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject child: %v", err)
	}
	if !parentInSource || !childInTarget {
		t.Fatalf("cross-project lineage ownership = parent-source:%t child-target:%t", parentInSource, childInTarget)
	}
	if !projectContainsWorkspaceRoot(t, fixture.metadata, targetProjectID, result.Binding.CanonicalRoot) {
		t.Fatalf("target project does not contain auto-attached workspace %q", result.Binding.CanonicalRoot)
	}
}

func TestSessionWorkspaceRetargeterSharedRootRemainsPersistable(t *testing.T) {
	tests := []struct {
		name         string
		crossProject bool
	}{
		{name: "default source project", crossProject: false},
		{name: "explicit target project", crossProject: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealSessionRetargetFixture(t)
			sharedRoot := fixture.targetWorkspaceRoot
			if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), fixture.sourceBinding.ProjectID, sharedRoot); err != nil {
				t.Fatalf("AttachWorkspaceToProject source: %v", err)
			}
			if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), fixture.targetProject.ProjectID, sharedRoot); err != nil {
				t.Fatalf("AttachWorkspaceToProject target: %v", err)
			}

			req := metadata.SessionWorkspaceRetargetRequest{
				SessionID:     fixture.child.Meta().SessionID,
				WorkspaceRoot: sharedRoot,
			}
			wantProjectID := fixture.sourceBinding.ProjectID
			if test.crossProject {
				targetProjectID := fixture.targetProject.ProjectID
				req.ProjectID = &targetProjectID
				wantProjectID = targetProjectID
			}

			result, err := fixture.retargeter(fixture.metadata).RetargetWorkspace(context.Background(), req)
			if err != nil {
				t.Fatalf("RetargetWorkspace: %v", err)
			}
			if result.Binding.ProjectID != wantProjectID {
				t.Fatalf("binding project = %q, want %q", result.Binding.ProjectID, wantProjectID)
			}
			if err := fixture.child.SetName("persisted after shared-root rebind"); err != nil {
				t.Fatalf("SetName after rebind: %v", err)
			}
			if _, _, err := fixture.child.AppendEvent("step-after-rebind", "message", map[string]string{"role": "user", "content": "after rebind"}); err != nil {
				t.Fatalf("AppendEvent after rebind: %v", err)
			}

			reopened, err := session.OpenByID(
				fixture.metadata.PersistenceRoot(),
				fixture.child.Meta().SessionID,
				fixture.metadata.AuthoritativeSessionStoreOptions()...,
			)
			if err != nil {
				t.Fatalf("OpenByID after rebind: %v", err)
			}
			if reopened.Meta().Name != "persisted after shared-root rebind" || reopened.Meta().LastSequence != 1 {
				t.Fatalf("reopened metadata = %+v", reopened.Meta())
			}
			belongs, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, wantProjectID)
			if err != nil {
				t.Fatalf("SessionBelongsToProject: %v", err)
			}
			if !belongs {
				t.Fatalf("session does not belong to selected project %q", wantProjectID)
			}
		})
	}
}

func TestSessionWorkspaceRetargeterStaleObserverCannotRestorePreviousTarget(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	observer := newBlockingSessionMetadataObserver(fixture.metadata)
	sourceContainer := filepath.Join(fixture.metadata.PersistenceRoot(), "projects", fixture.sourceBinding.ProjectID, "sessions")
	store, err := session.Create(
		sourceContainer,
		fixture.sourceBinding.WorkspaceName,
		fixture.sourceBinding.CanonicalRoot,
		session.WithPersistenceObserver(observer),
		session.WithPersistedSessionResolver(fixture.metadata),
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	fixture.stores.RegisterStore(store)

	observer.Arm()
	persistDone := make(chan error, 1)
	go func() {
		persistDone <- store.SetName("captured before rebind")
	}()
	select {
	case <-observer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("metadata observer did not block")
	}

	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     store.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
	}
	type retargetOutcome struct {
		result metadata.SessionWorkspaceRetargetResult
		err    error
	}
	retargetDone := make(chan retargetOutcome, 1)
	go func() {
		result, err := fixture.retargeter(fixture.metadata).RetargetWorkspace(context.Background(), req)
		retargetDone <- retargetOutcome{result: result, err: err}
	}()
	close(observer.release)
	if err := <-persistDone; err != nil {
		t.Fatalf("SetName observer completion: %v", err)
	}
	retargeted := <-retargetDone
	if retargeted.err != nil {
		t.Fatalf("RetargetWorkspace: %v", retargeted.err)
	}

	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		store.Meta().SessionID,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != retargeted.result.Binding.CanonicalRoot {
		t.Fatalf("workspace root = %q, want rebound root %q", reopened.Meta().WorkspaceRoot, retargeted.result.Binding.CanonicalRoot)
	}
	if reopened.Meta().WorkspaceContainer != retargeted.result.Binding.WorkspaceName {
		t.Fatalf("workspace container = %q, want %q", reopened.Meta().WorkspaceContainer, retargeted.result.Binding.WorkspaceName)
	}
	if reopened.Meta().Name != "captured before rebind" {
		t.Fatalf("session name = %q, want pre-rebind metadata mutation", reopened.Meta().Name)
	}
	target, err := fixture.metadata.ResolveSessionExecutionTarget(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != retargeted.result.Binding.WorkspaceID {
		t.Fatalf("workspace id = %q, want rebound workspace %q", target.WorkspaceID, retargeted.result.Binding.WorkspaceID)
	}
}

type failingSessionRetargetCommit struct {
	*metadata.Store
	err error
}

func (s failingSessionRetargetCommit) CommitSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetPlan, time.Time) (metadata.SessionWorkspaceRetargetResult, error) {
	return metadata.SessionWorkspaceRetargetResult{}, s.err
}

func TestSessionWorkspaceRetargeterRestoresRealArtifactAndOwnershipAfterCommitFailure(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	plan, err := fixture.metadata.PlanSessionWorkspaceRetarget(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	commitErr := errors.New("commit failed")
	metadataSource := failingSessionRetargetCommit{Store: fixture.metadata, err: commitErr}

	_, err = fixture.retargeter(metadataSource).RetargetWorkspace(context.Background(), req)
	if !errors.Is(err, commitErr) {
		t.Fatalf("RetargetWorkspace error = %v, want %v", err, commitErr)
	}
	if info, err := os.Stat(plan.SourceSessionDir); err != nil || !info.IsDir() {
		t.Fatalf("source artifact was not restored: %v, %v", info, err)
	}
	if _, err := os.Stat(plan.TargetSessionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target artifact still exists: %v", err)
	}
	if canonicalRetargetTestPath(t, fixture.child.Dir()) != canonicalRetargetTestPath(t, plan.SourceSessionDir) {
		t.Fatalf("session store dir = %q, want unchanged %q", fixture.child.Dir(), plan.SourceSessionDir)
	}
	childInSource, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, fixture.sourceBinding.ProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject source: %v", err)
	}
	childInTarget, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, targetProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject target: %v", err)
	}
	if !childInSource || childInTarget {
		t.Fatalf("failed rebind ownership = source:%t target:%t", childInSource, childInTarget)
	}
	if projectContainsWorkspaceRoot(t, fixture.metadata, targetProjectID, fixture.targetWorkspaceRoot) {
		t.Fatalf("failed rebind attached workspace %q", fixture.targetWorkspaceRoot)
	}
}
