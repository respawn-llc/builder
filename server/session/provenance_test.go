package session

import (
	"testing"

	"core/shared/runtimeids"
)

func TestInitializeCreationContextSetsTypedProvenanceAtomically(t *testing.T) {
	root := t.TempDir()
	parentAgent := newSessionTestStoreAt(t, root)
	source := newSessionTestLazyStoreAt(t, root)
	if err := InitializeCreationContext(source, parentAgent, SessionCreationSourceParentAgent, ChildContextOptions{}); err != nil {
		t.Fatalf("initialize delegated source: %v", err)
	}

	tests := []struct {
		name            string
		source          *Store
		kind            SessionCreationSourceKind
		wantPrevious    *runtimeids.SessionID
		wantParentAgent *runtimeids.SessionID
	}{
		{name: "independent", kind: SessionCreationSourceIndependent},
		{
			name:            "previous session preserves delegation ancestry",
			source:          source,
			kind:            SessionCreationSourcePreviousSession,
			wantPrevious:    mustProvenanceSessionID(t, source.Metadata().SessionID),
			wantParentAgent: mustProvenanceSessionID(t, parentAgent.Metadata().SessionID),
		},
		{
			name:            "parent agent records immediate caller",
			source:          source,
			kind:            SessionCreationSourceParentAgent,
			wantParentAgent: mustProvenanceSessionID(t, source.Metadata().SessionID),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			child := newSessionTestLazyStoreAt(t, root)
			if err := InitializeCreationContext(child, test.source, test.kind, ChildContextOptions{}); err != nil {
				t.Fatalf("InitializeCreationContext: %v", err)
			}
			meta := child.Metadata()
			assertOptionalProvenanceID(t, "previous", meta.PreviousSessionID, test.wantPrevious)
			assertOptionalProvenanceID(t, "parent agent", meta.ParentAgentSessionID, test.wantParentAgent)
			if test.source != nil &&
				(meta.WorkspaceRoot != test.source.Metadata().WorkspaceRoot ||
					meta.WorkspaceContainer != test.source.Metadata().WorkspaceContainer) {
				t.Fatalf("workspace context = %q/%q, want source %q/%q",
					meta.WorkspaceRoot,
					meta.WorkspaceContainer,
					test.source.Metadata().WorkspaceRoot,
					test.source.Metadata().WorkspaceContainer,
				)
			}
		})
	}
}

func TestForkAndCloneRecordPreviousSessionAndPreserveParentAgentAncestry(t *testing.T) {
	root := t.TempDir()
	parentAgent := newSessionTestStoreAt(t, root)
	source := newSessionTestLazyStoreAt(t, root)
	if err := InitializeCreationContext(source, parentAgent, SessionCreationSourceParentAgent, ChildContextOptions{}); err != nil {
		t.Fatalf("initialize source ancestry: %v", err)
	}
	if err := source.EnsureDurable(); err != nil {
		t.Fatalf("persist source ancestry: %v", err)
	}
	sourceLog := mustMaterializeSessionTestEventLog(t, source)
	target, _, err := sourceLog.AppendRecord(stringPointer("step"), userMessagePayload(t, "fork target"))
	if err != nil {
		t.Fatalf("append fork target: %v", err)
	}

	forked, _, err := ForkAtUserMessage(sourceLog, target.Seq(), "forked", testSessionCategory)
	if err != nil {
		t.Fatalf("ForkAtUserMessage: %v", err)
	}
	cloned, err := CloneSession(sourceLog, "cloned", testSessionCategory)
	if err != nil {
		t.Fatalf("CloneSession: %v", err)
	}
	for name, derived := range map[string]*Store{"forked": forked, "cloned": cloned} {
		meta := derived.Metadata()
		assertOptionalProvenanceID(t, name+" previous", meta.PreviousSessionID, mustProvenanceSessionID(t, source.Metadata().SessionID))
		assertOptionalProvenanceID(t, name+" parent agent", meta.ParentAgentSessionID, mustProvenanceSessionID(t, parentAgent.Metadata().SessionID))
	}
}

func mustProvenanceSessionID(t *testing.T, raw string) *runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return &id
}

func assertOptionalProvenanceID(t *testing.T, label string, got *runtimeids.SessionID, want *runtimeids.SessionID) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s ID = %q, want absent", label, got.String())
		}
		return
	}
	if got == nil || *got != *want {
		t.Fatalf("%s ID = %v, want %q", label, got, want.String())
	}
}

func TestNavigationTargetSessionIDPrefersPreviousAndReturnsCopy(t *testing.T) {
	previous := mustProvenanceSessionID(t, "previous-session")
	parentAgent := mustProvenanceSessionID(t, "parent-agent-session")
	meta := Metadata{PreviousSessionID: previous, ParentAgentSessionID: parentAgent}

	target := NavigationTargetSessionID(meta)
	assertOptionalProvenanceID(t, "navigation target", target, previous)
	if target == previous {
		t.Fatal("navigation target aliases persisted provenance")
	}
}

func TestNavigationTargetSessionIDFallsBackToParentAgent(t *testing.T) {
	parentAgent := mustProvenanceSessionID(t, "parent-agent-session")
	target := NavigationTargetSessionID(Metadata{ParentAgentSessionID: parentAgent})
	assertOptionalProvenanceID(t, "navigation target", target, parentAgent)
}
