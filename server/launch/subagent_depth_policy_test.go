package launch

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestPlannerEnforcesParentAgentDepthAcrossRoles(t *testing.T) {
	planner, containerDir, persistence := newSubagentDepthPlanner(t, 2)
	root := createSubagentDepthSession(t, containerDir, persistence, nil, "root")
	child := createSubagentDepthSession(t, containerDir, persistence, root, "worker")
	grandchild := createSubagentDepthSession(t, containerDir, persistence, child, "reviewer")

	permitted, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, child))
	if err != nil {
		t.Fatalf("plan permitted depth-2 child: %v", err)
	}
	if parent := permitted.Store.Meta().ParentAgentSessionID; parent == nil || parent.String() != child.Meta().SessionID {
		t.Fatalf("permitted child parent-agent session = %v, want %q", parent, child.Meta().SessionID)
	}

	before := requireSessionDirectoryNames(t, containerDir)
	_, err = planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, grandchild))
	assertMaxDepthPolicyError(t, err, 3, 2)
	after := requireSessionDirectoryNames(t, containerDir)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected depth-3 launch changed session directories: before=%v after=%v", before, after)
	}
}

func TestPlannerMaximumZeroRejectsFirstParentAgentChildButNotIndependentCreation(t *testing.T) {
	planner, containerDir, persistence := newSubagentDepthPlanner(t, 0)
	root := createSubagentDepthSession(t, containerDir, persistence, nil, "")

	before := requireSessionDirectoryNames(t, containerDir)
	_, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, root))
	assertMaxDepthPolicyError(t, err, 1, 0)
	if after := requireSessionDirectoryNames(t, containerDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("maximum-zero rejection changed session directories: before=%v after=%v", before, after)
	}

	independent, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if err != nil {
		t.Fatalf("plan independent scheduler/root session: %v", err)
	}
	if independent.Store.Meta().ParentAgentSessionID != nil {
		t.Fatalf("independent session parent-agent provenance = %v, want absent", independent.Store.Meta().ParentAgentSessionID)
	}
}

func TestPlannerTreatsWorkflowAgentSessionAsDepthZeroRoot(t *testing.T) {
	planner, containerDir, persistence := newSubagentDepthPlanner(t, 1)
	workflowRoot := createSubagentDepthSession(t, containerDir, persistence, nil, "")
	if err := workflowRoot.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}

	plan, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, workflowRoot))
	if err != nil {
		t.Fatalf("workflow-agent child at depth 1: %v", err)
	}
	if parent := plan.Store.Meta().ParentAgentSessionID; parent == nil || parent.String() != workflowRoot.Meta().SessionID {
		t.Fatalf("workflow child parent-agent session = %v, want %q", parent, workflowRoot.Meta().SessionID)
	}
}

func TestPlannerParentAgentAncestryFailureMatrix(t *testing.T) {
	corruptRecordErr := errors.New("persisted session record corrupt")
	unavailableStoreErr := errors.New("persisted session store unavailable")
	tests := []struct {
		name          string
		ancestorError error
		wantError     error
	}{
		{name: "older missing ancestor ends lineage", ancestorError: session.ErrSessionNotFound},
		{name: "permission failure blocks launch", ancestorError: os.ErrPermission, wantError: os.ErrPermission},
		{name: "io failure blocks launch", ancestorError: io.ErrUnexpectedEOF, wantError: io.ErrUnexpectedEOF},
		{name: "corrupt record blocks launch", ancestorError: corruptRecordErr, wantError: corruptRecordErr},
		{name: "unavailable store blocks launch", ancestorError: unavailableStoreErr, wantError: unavailableStoreErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planner, containerDir, persistence := newSubagentDepthPlanner(t, 2)
			root := createSubagentDepthSession(t, containerDir, persistence, nil, "")
			caller := createSubagentDepthSession(t, containerDir, persistence, root, "")
			rootID := mustSubagentDepthSessionID(t, root.Meta().SessionID)
			resolver := &subagentDepthResolver{
				base: persistence,
				errs: map[string]error{rootID.String(): test.ancestorError},
			}
			planner.PersistedSessions = resolver

			plan, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("PlanSession error = %v, want %v", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanSession with missing older ancestor: %v", err)
			}
			if plan.Store == nil {
				t.Fatal("missing older ancestor did not produce a child session")
			}
		})
	}
}

func TestPlannerResolvesParentAgentDepthAcrossProjectContainers(t *testing.T) {
	root := t.TempDir()
	containerA := filepath.Join(root, "projects", "project-a", "sessions")
	containerB := filepath.Join(root, "projects", "project-b", "sessions")
	persistence := sessiontest.NewPersistence()
	lineageRoot := createSubagentDepthSession(t, containerB, persistence, nil, "")
	caller := createSubagentDepthSession(t, containerB, persistence, lineageRoot, "")
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
			Settings: config.Settings{
				Model:            "gpt-5",
				MaxSubagentDepth: 1,
			},
		},
		ContainerDir:      containerA,
		StoreOptions:      persistence.Options(),
		PersistedSessions: persistence,
	}

	_, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
	assertMaxDepthPolicyError(t, err, 2, 1)
	if entries := requireSessionDirectoryNames(t, containerA); len(entries) != 0 {
		t.Fatalf("cross-project rejection created project-a session directories: %v", entries)
	}
}

func TestPlannerImmediateMissingParentAgentRemainsACallerContextFailure(t *testing.T) {
	planner, _, _ := newSubagentDepthPlanner(t, 2)
	missing := mustSubagentDepthSessionID(t, "missing-immediate-caller")
	_, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(missing)),
	})
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("PlanSession error = %T %v, want immediate session-not-found failure", err, err)
	}
}

func TestPlannerStopsBeforeResolvingAncestorOnceLimitIsExceeded(t *testing.T) {
	planner, containerDir, persistence := newSubagentDepthPlanner(t, 1)
	root := createSubagentDepthSession(t, containerDir, persistence, nil, "")
	caller := createSubagentDepthSession(t, containerDir, persistence, root, "")
	resolver := &subagentDepthResolver{
		base: persistence,
		errs: map[string]error{root.Meta().SessionID: errors.New("must not resolve root after depth is known")},
	}
	planner.PersistedSessions = resolver

	_, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
	assertMaxDepthPolicyError(t, err, 2, 1)
	if want := []string{caller.Meta().SessionID}; !reflect.DeepEqual(resolver.calls, want) {
		t.Fatalf("resolver calls = %v, want only immediate caller %v", resolver.calls, want)
	}
}

func TestPlannerReadsOlderAncestryAsMetadataWithoutOpeningTranscript(t *testing.T) {
	planner, containerDir, persistence := newSubagentDepthPlanner(t, 2)
	caller := createSubagentDepthSession(t, containerDir, persistence, nil, "")
	syntheticAncestorID := mustSubagentDepthSessionID(t, "metadata-only-ancestor")
	callerRecord, err := persistence.ResolvePersistedSession(context.Background(), caller.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession caller: %v", err)
	}
	callerRecord.Meta = cloneSubagentDepthMeta(callerRecord.Meta)
	callerRecord.Meta.ParentAgentSessionID = &syntheticAncestorID
	resolver := &subagentDepthResolver{
		base: persistence,
		records: map[string]session.PersistedSessionRecord{
			caller.Meta().SessionID: callerRecord,
			syntheticAncestorID.String(): {
				SessionDir: filepath.Join(t.TempDir(), "must-not-open"),
				Meta: &session.Meta{
					SessionID: syntheticAncestorID.String(),
					Category:  sessionCategoryPointerForDepthTest(sessioncontract.SessionCategorySubagent),
				},
			},
		},
	}
	planner.PersistedSessions = resolver

	plan, err := planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
	if err != nil {
		t.Fatalf("PlanSession metadata-only ancestry: %v", err)
	}
	if plan.Store == nil {
		t.Fatal("metadata-only ancestry did not create a child")
	}
	if want := []string{caller.Meta().SessionID, syntheticAncestorID.String()}; !reflect.DeepEqual(resolver.calls, want) {
		t.Fatalf("resolver calls = %v, want %v", resolver.calls, want)
	}
}

func TestPlannerRejectsCorruptParentAgentLineage(t *testing.T) {
	t.Run("self cycle returns typed error in production", func(t *testing.T) {
		planner, containerDir, persistence := newSubagentDepthPlanner(t, 2)
		caller := createSubagentDepthSession(t, containerDir, persistence, nil, "")
		callerID := mustSubagentDepthSessionID(t, caller.Meta().SessionID)
		callerRecord, err := persistence.ResolvePersistedSession(context.Background(), caller.Meta().SessionID)
		if err != nil {
			t.Fatalf("ResolvePersistedSession caller: %v", err)
		}
		callerRecord.Meta = cloneSubagentDepthMeta(callerRecord.Meta)
		callerRecord.Meta.ParentAgentSessionID = &callerID
		planner.PersistedSessions = &subagentDepthResolver{
			base:    persistence,
			records: map[string]session.PersistedSessionRecord{caller.Meta().SessionID: callerRecord},
		}

		_, err = planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
		assertLineagePolicyError(t, err, callerID, []runtimeids.SessionID{callerID})
	})

	t.Run("multi-node cycle returns typed error in production", func(t *testing.T) {
		planner, containerDir, persistence := newSubagentDepthPlanner(t, 3)
		caller := createSubagentDepthSession(t, containerDir, persistence, nil, "")
		callerID := mustSubagentDepthSessionID(t, caller.Meta().SessionID)
		ancestorID := mustSubagentDepthSessionID(t, "cycle-ancestor")
		callerRecord, err := persistence.ResolvePersistedSession(context.Background(), caller.Meta().SessionID)
		if err != nil {
			t.Fatalf("ResolvePersistedSession caller: %v", err)
		}
		callerRecord.Meta = cloneSubagentDepthMeta(callerRecord.Meta)
		callerRecord.Meta.ParentAgentSessionID = &ancestorID
		resolver := &subagentDepthResolver{
			base: persistence,
			records: map[string]session.PersistedSessionRecord{
				caller.Meta().SessionID: callerRecord,
				ancestorID.String(): {
					SessionDir: filepath.Join(t.TempDir(), "must-not-open"),
					Meta: &session.Meta{
						SessionID:            ancestorID.String(),
						Category:             sessionCategoryPointerForDepthTest(sessioncontract.SessionCategorySubagent),
						ParentAgentSessionID: &callerID,
					},
				},
			},
		}
		planner.PersistedSessions = resolver

		_, err = planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
		assertLineagePolicyError(t, err, callerID, []runtimeids.SessionID{callerID, ancestorID})
	})

	t.Run("debug mode panics with typed diagnostic facts", func(t *testing.T) {
		planner, containerDir, persistence := newSubagentDepthPlanner(t, 2)
		planner.Config.Settings.Debug = true
		caller := createSubagentDepthSession(t, containerDir, persistence, nil, "")
		callerID := mustSubagentDepthSessionID(t, caller.Meta().SessionID)
		callerRecord, err := persistence.ResolvePersistedSession(context.Background(), caller.Meta().SessionID)
		if err != nil {
			t.Fatalf("ResolvePersistedSession caller: %v", err)
		}
		callerRecord.Meta = cloneSubagentDepthMeta(callerRecord.Meta)
		callerRecord.Meta.ParentAgentSessionID = &callerID
		planner.PersistedSessions = &subagentDepthResolver{
			base:    persistence,
			records: map[string]session.PersistedSessionRecord{caller.Meta().SessionID: callerRecord},
		}

		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_, _ = planner.PlanSession(context.Background(), parentAgentLaunchRequest(t, caller))
		}()
		policy, ok := recovered.(*protocol.SubagentLaunchPolicyError)
		if !ok {
			t.Fatalf("panic value = %T %v, want typed launch policy error", recovered, recovered)
		}
		assertLineagePolicyError(t, policy, callerID, []runtimeids.SessionID{callerID})
	})
}

func TestPlannerOpenExistingIsUnaffectedByMaximumDepth(t *testing.T) {
	planner, containerDir, persistence := newSubagentDepthPlanner(t, 0)
	root := createSubagentDepthSession(t, containerDir, persistence, nil, "")
	child := createSubagentDepthSession(t, containerDir, persistence, root, "")
	childID := mustSubagentDepthSessionID(t, child.Meta().SessionID)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(childID),
	})
	if err != nil {
		t.Fatalf("open existing nested session: %v", err)
	}
	if plan.Store.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("opened session = %q, want %q", plan.Store.Meta().SessionID, child.Meta().SessionID)
	}
}

func newSubagentDepthPlanner(t *testing.T, maxDepth int) (Planner, string, *sessiontest.Persistence) {
	t.Helper()
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	return Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
			Settings: config.Settings{
				Model:            "gpt-5",
				MaxSubagentDepth: maxDepth,
			},
		},
		ContainerDir:      containerDir,
		StoreOptions:      persistence.Options(),
		PersistedSessions: persistence,
	}, containerDir, persistence
}

func createSubagentDepthSession(t *testing.T, containerDir string, persistence *sessiontest.Persistence, parent *session.Store, role string) *session.Store {
	t.Helper()
	category := sessioncontract.SessionCategoryMain
	if parent != nil {
		category = sessioncontract.SessionCategorySubagent
	}
	store, err := session.NewLazy(containerDir, filepath.Base(containerDir), "/tmp/workspace-a", category, persistence.Options()...)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	if parent == nil {
		err = session.InitializeCreationContext(store, nil, session.SessionCreationSourceIndependent, session.ChildContextOptions{})
	} else {
		err = session.InitializeCreationContext(store, parent, session.SessionCreationSourceParentAgent, session.ChildContextOptions{})
	}
	if err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	if role != "" {
		if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
			t.Fatalf("SetContinuationContext: %v", err)
		}
	}
	return store
}

func parentAgentLaunchRequest(t *testing.T, caller *session.Store) SessionRequest {
	t.Helper()
	callerID := mustSubagentDepthSessionID(t, caller.Meta().SessionID)
	return SessionRequest{
		Mode:   ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(callerID)),
	}
}

func assertMaxDepthPolicyError(t *testing.T, err error, attemptedDepth, maxDepth int) {
	t.Helper()
	var policy *protocol.SubagentLaunchPolicyError
	if !errors.As(err, &policy) {
		t.Fatalf("error = %T %v, want SubagentLaunchPolicyError", err, err)
	}
	if policy.Kind != protocol.SubagentLaunchPolicyMaxDepthExceeded ||
		policy.AttemptedDepth == nil || *policy.AttemptedDepth != attemptedDepth ||
		policy.MaxDepth == nil || *policy.MaxDepth != maxDepth {
		t.Fatalf("policy error = %+v, want attempted depth %d maximum %d", policy, attemptedDepth, maxDepth)
	}
}

func assertLineagePolicyError(t *testing.T, err error, repeated runtimeids.SessionID, visited []runtimeids.SessionID) {
	t.Helper()
	var policy *protocol.SubagentLaunchPolicyError
	if !errors.As(err, &policy) {
		t.Fatalf("error = %T %v, want SubagentLaunchPolicyError", err, err)
	}
	if policy.Kind != protocol.SubagentLaunchPolicyLineageCorrupt ||
		policy.RepeatedSessionID == nil || *policy.RepeatedSessionID != repeated ||
		!reflect.DeepEqual(policy.VisitedSessionIDs, visited) {
		t.Fatalf("lineage policy error = %+v, want repeated %q visited %v", policy, repeated, visited)
	}
}

func requireSessionDirectoryNames(t *testing.T, containerDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(containerDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", containerDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

type subagentDepthResolver struct {
	base    session.PersistedSessionResolver
	records map[string]session.PersistedSessionRecord
	errs    map[string]error
	calls   []string
}

func (r *subagentDepthResolver) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	r.calls = append(r.calls, sessionID)
	if err := r.errs[sessionID]; err != nil {
		return session.PersistedSessionRecord{}, err
	}
	if record, ok := r.records[sessionID]; ok {
		record.Meta = cloneSubagentDepthMeta(record.Meta)
		return record, nil
	}
	return r.base.ResolvePersistedSession(ctx, sessionID)
}

func cloneSubagentDepthMeta(meta *session.Meta) *session.Meta {
	if meta == nil {
		return nil
	}
	cloned := *meta
	if meta.PreviousSessionID != nil {
		id := *meta.PreviousSessionID
		cloned.PreviousSessionID = &id
	}
	if meta.ParentAgentSessionID != nil {
		id := *meta.ParentAgentSessionID
		cloned.ParentAgentSessionID = &id
	}
	return &cloned
}

func sessionCategoryPointerForDepthTest(category sessioncontract.SessionCategory) *sessioncontract.SessionCategory {
	copied := category
	return &copied
}

func mustSubagentDepthSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
