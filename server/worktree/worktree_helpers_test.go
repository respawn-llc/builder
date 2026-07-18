package worktree

import (
	"encoding/json"
	"strings"
	"testing"

	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestLocalBranchDerivesNameFromCanonicalRef(t *testing.T) {
	branch, err := newLocalBranch("refs/heads/feature/round-trip")
	if err != nil {
		t.Fatalf("newLocalBranch: %v", err)
	}
	if branch.Ref() != "refs/heads/feature/round-trip" {
		t.Fatalf("branch ref = %q", branch.Ref())
	}
	if branch.Name() != "feature/round-trip" {
		t.Fatalf("branch name = %q", branch.Name())
	}
}

func TestLocalBranchRejectsInvalidRefs(t *testing.T) {
	for _, ref := range []string{"", " ", "main", "refs/tags/v1", "refs/heads/", "refs/heads/ "} {
		t.Run(strings.ReplaceAll(ref, "/", "_"), func(t *testing.T) {
			if _, err := newLocalBranch(ref); err == nil {
				t.Fatalf("newLocalBranch(%q) succeeded", ref)
			}
		})
	}
}

func TestGitMetadataRoundTripsNamedDetachedAndUnknownHeads(t *testing.T) {
	namedBranch := mustLocalBranch(t, "refs/heads/feature/round-trip")
	tests := []struct {
		name   string
		source GitWorktree
		assert func(*testing.T, GitWorktree)
	}{
		{
			name: "named",
			source: GitWorktree{
				HeadOID: "named-head",
				Branch:  namedBranch,
			},
			assert: func(t *testing.T, decoded GitWorktree) {
				t.Helper()
				if decoded.Branch == nil || decoded.Branch.Ref() != namedBranch.Ref() || decoded.Branch.Name() != namedBranch.Name() {
					t.Fatalf("decoded named branch = %+v", decoded.Branch)
				}
				if decoded.Detached {
					t.Fatal("decoded named head is detached")
				}
			},
		},
		{
			name: "detached",
			source: GitWorktree{
				HeadOID:  "detached-head",
				Detached: true,
			},
			assert: func(t *testing.T, decoded GitWorktree) {
				t.Helper()
				if !decoded.Detached || decoded.Branch != nil {
					t.Fatalf("decoded detached head = %+v", decoded)
				}
			},
		},
		{
			name:   "unknown",
			source: GitWorktree{HeadOID: "unknown-head"},
			assert: func(t *testing.T, decoded GitWorktree) {
				t.Helper()
				if decoded.Detached || decoded.Branch != nil {
					t.Fatalf("decoded unknown head = %+v", decoded)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := marshalGitMetadata(tt.source)
			if err != nil {
				t.Fatalf("marshalGitMetadata: %v", err)
			}
			decoded, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: encoded})
			if err != nil {
				t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
			}
			if decoded.HeadOID != tt.source.HeadOID {
				t.Fatalf("decoded head = %q, want %q", decoded.HeadOID, tt.source.HeadOID)
			}
			tt.assert(t, decoded)
			var wire map[string]json.RawMessage
			if err := json.Unmarshal([]byte(encoded), &wire); err != nil {
				t.Fatalf("decode wire metadata: %v", err)
			}
			switch tt.name {
			case "named":
				if string(wire["branch_ref"]) != `"refs/heads/feature/round-trip"` ||
					string(wire["branch_name"]) != `"feature/round-trip"` ||
					wire["detached"] != nil {
					t.Fatalf("named wire metadata = %s", encoded)
				}
			case "detached":
				if wire["branch_ref"] != nil || wire["branch_name"] != nil || string(wire["detached"]) != "true" {
					t.Fatalf("detached wire metadata = %s", encoded)
				}
			case "unknown":
				if wire["branch_ref"] != nil || wire["branch_name"] != nil || wire["detached"] != nil {
					t.Fatalf("unknown wire metadata = %s", encoded)
				}
			}
		})
	}
}

func TestGitMetadataDecodesValidLegacyHeadShapes(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		assert   func(*testing.T, GitWorktree)
	}{
		{
			name:     "named",
			metadata: `{"head_oid":"named-head","branch_ref":"refs/heads/feature/legacy","branch_name":"feature/legacy"}`,
			assert: func(t *testing.T, decoded GitWorktree) {
				t.Helper()
				if decoded.Branch == nil || decoded.Branch.Ref() != "refs/heads/feature/legacy" || decoded.Branch.Name() != "feature/legacy" {
					t.Fatalf("decoded named branch = %+v", decoded.Branch)
				}
			},
		},
		{
			name:     "detached",
			metadata: `{"head_oid":"detached-head","detached":true}`,
			assert: func(t *testing.T, decoded GitWorktree) {
				t.Helper()
				if !decoded.Detached || decoded.Branch != nil {
					t.Fatalf("decoded detached head = %+v", decoded)
				}
			},
		},
		{
			name:     "unknown",
			metadata: `{}`,
			assert: func(t *testing.T, decoded GitWorktree) {
				t.Helper()
				if decoded.Detached || decoded.Branch != nil {
					t.Fatalf("decoded unknown head = %+v", decoded)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: tt.metadata})
			if err != nil {
				t.Fatalf("worktreeGitMetadataFromRecord: %v", err)
			}
			tt.assert(t, decoded)
		})
	}
}

func TestGitMetadataRejectsMalformedHeadShapes(t *testing.T) {
	namedBranch := mustLocalBranch(t, "refs/heads/feature/invalid-detached")
	if _, err := marshalGitMetadata(GitWorktree{Branch: namedBranch, Detached: true}); err == nil {
		t.Fatal("marshalGitMetadata accepted detached head with a named branch")
	}
	for _, metadataJSON := range []string{
		`{"branch_ref":""}`,
		`{"branch_name":""}`,
		`{"branch_ref":"refs/heads/feature/partial"}`,
		`{"branch_name":"feature/partial"}`,
		`{"branch_ref":"refs/tags/v1","branch_name":"v1"}`,
		`{"branch_ref":"refs/heads/feature/ref","branch_name":"feature/other"}`,
		`{"branch_ref":"refs/heads/feature/detached","branch_name":"feature/detached","detached":true}`,
	} {
		t.Run(metadataJSON, func(t *testing.T) {
			if _, err := worktreeGitMetadataFromRecord(metadata.WorktreeRecord{GitMetadataJSON: metadataJSON}); err == nil {
				t.Fatalf("worktreeGitMetadataFromRecord accepted %s", metadataJSON)
			}
		})
	}
}

func TestPlanWorktreeDeletionDerivesLiveNamedHeadDecisions(t *testing.T) {
	for _, variant := range []serverapi.WorktreeTopologyVariant{
		serverapi.WorktreeTopologyVariantRegistered,
		serverapi.WorktreeTopologyVariantExternal,
	} {
		t.Run(string(variant), func(t *testing.T) {
			const branchRef = "refs/heads/feature/delete-plan"
			const branchName = "feature/delete-plan"
			facts := serverapi.WorktreeGitFacts{
				CanonicalRoot: "/repo/worktree",
				HeadObject:    "deadbeef",
				BranchRef:     testStringPointer(branchRef),
				BranchName:    testStringPointer(branchName),
				PathAvailable: true,
			}
			entry := serverapi.WorktreeTopologyEntry{Variant: variant}
			var record *metadata.WorktreeRecord
			switch variant {
			case serverapi.WorktreeTopologyVariantRegistered:
				entry.Registered = &serverapi.WorktreeRegisteredFacts{
					Git: facts,
					Kent: serverapi.WorktreeKentFacts{
						WorktreeID:    "worktree-id",
						CanonicalRoot: facts.CanonicalRoot,
						DisplayName:   "worktree",
					},
				}
				record = &metadata.WorktreeRecord{ID: "worktree-id", CanonicalRoot: facts.CanonicalRoot}
			case serverapi.WorktreeTopologyVariantExternal:
				entry.External = &serverapi.WorktreeExternalFacts{Git: facts}
			}

			plan, err := planWorktreeDeletion(entry, record, serverapi.WorktreeBranchCleanupModeDeleteSafe)
			if err != nil {
				t.Fatalf("planWorktreeDeletion: %v", err)
			}
			if plan.cleanup.kind != worktreeBranchCleanupDelete || plan.cleanup.branch == nil || plan.cleanup.branch.Name() != branchName {
				t.Fatalf("cleanup decision = %+v", plan.cleanup)
			}
			if plan.exitReminder.branch == nil || plan.exitReminder.branch.Name() != branchName || plan.exitReminder.worktreePath != facts.CanonicalRoot {
				t.Fatalf("exit reminder projection = %+v", plan.exitReminder)
			}
		})
	}
}

func TestPlanWorktreeDeletionIgnoresPersistedIdentityForMissingTopology(t *testing.T) {
	for _, metadataJSON := range []string{
		`{"branch_ref":"refs/heads/feature/stale","branch_name":"feature/stale"}`,
		`{"branch_ref":"refs/heads/feature/malformed","branch_name":"feature/other"}`,
	} {
		t.Run(metadataJSON, func(t *testing.T) {
			record := metadata.WorktreeRecord{
				ID:              "worktree-id",
				CanonicalRoot:   "/repo/missing-worktree",
				GitMetadataJSON: metadataJSON,
			}
			entry := serverapi.WorktreeTopologyEntry{
				Variant: serverapi.WorktreeTopologyVariantMissing,
				Missing: &serverapi.WorktreeMissingFacts{
					Kent: serverapi.WorktreeKentFacts{
						WorktreeID:    record.ID,
						CanonicalRoot: record.CanonicalRoot,
						DisplayName:   "missing-worktree",
					},
				},
			}

			plan, err := planWorktreeDeletion(entry, &record, serverapi.WorktreeBranchCleanupModeDeleteSafe)
			if err != nil {
				t.Fatalf("planWorktreeDeletion: %v", err)
			}
			if plan.cleanup.kind != worktreeBranchCleanupNotApplicable || plan.cleanup.branch != nil {
				t.Fatalf("cleanup decision = %+v", plan.cleanup)
			}
			if plan.exitReminder.branch != nil || plan.exitReminder.worktreePath != record.CanonicalRoot {
				t.Fatalf("exit reminder projection = %+v", plan.exitReminder)
			}
		})
	}
}

func TestWorktreeReminderTransitionProjectsNamedDetachedAndUnknownHeads(t *testing.T) {
	namedBranch := mustLocalBranch(t, "refs/heads/feature/reminder")
	tests := []struct {
		name   string
		head   GitWorktree
		branch *string
	}{
		{
			name:   "named",
			head:   GitWorktree{Branch: namedBranch},
			branch: testStringPointer(namedBranch.Name()),
		},
		{
			name: "detached",
			head: GitWorktree{Detached: true},
		},
		{
			name: "unknown",
			head: GitWorktree{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := syncedWorktree{
				record: metadata.WorktreeRecord{ID: "worktree-id", CanonicalRoot: "/repo/worktree"},
				git:    tt.head,
			}
			nextTarget := clientui.SessionExecutionTarget{
				WorkspaceRoot:    "/repo",
				EffectiveWorkdir: "/repo/worktree",
				Worktree:         &clientui.SessionExecutionWorktreeTarget{ID: "worktree-id", Root: "/repo/worktree"},
			}
			reminder, ok, err := worktreeReminderStateForTransition(nil, clientui.SessionExecutionTarget{}, next, nextTarget)
			if err != nil {
				t.Fatalf("worktreeReminderStateForTransition: %v", err)
			}
			if !ok {
				t.Fatal("worktree transition omitted enter reminder")
			}
			switch {
			case tt.branch == nil && reminder.Branch != nil:
				t.Fatalf("reminder branch = %q, want absent", *reminder.Branch)
			case tt.branch != nil && (reminder.Branch == nil || *reminder.Branch != *tt.branch):
				t.Fatalf("reminder branch = %+v, want %q", reminder.Branch, *tt.branch)
			}
		})
	}
}

func TestWorktreeReminderTransitionRejectsPresentPreviousTargetWithEmptyWorktreeID(t *testing.T) {
	previous := &syncedWorktree{
		record: metadata.WorktreeRecord{CanonicalRoot: "/repo/worktree"},
		git:    GitWorktree{IsMain: false},
	}
	next := syncedWorktree{
		record: metadata.WorktreeRecord{CanonicalRoot: "/repo"},
		git:    GitWorktree{IsMain: true},
	}
	previousTarget := clientui.SessionExecutionTarget{
		WorkspaceRoot: "/repo",
		Worktree:      &clientui.SessionExecutionWorktreeTarget{},
	}
	nextTarget := clientui.SessionExecutionTarget{
		WorkspaceRoot:    "/repo",
		EffectiveWorkdir: "/repo",
	}

	if _, _, err := worktreeReminderStateForTransition(previous, previousTarget, next, nextTarget); err == nil {
		t.Fatal("expected transition reminder to reject present previous worktree target without id")
	}
}

func mustLocalBranch(t *testing.T, ref string) *localBranch {
	t.Helper()
	branch, err := newLocalBranch(ref)
	if err != nil {
		t.Fatalf("newLocalBranch(%q): %v", ref, err)
	}
	return &branch
}

func testStringPointer(value string) *string {
	return &value
}
