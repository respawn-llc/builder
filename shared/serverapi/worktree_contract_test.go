package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/clientui"
)

func TestWorktreeTopologyEntryRequiresExactlyOneMatchingPayload(t *testing.T) {
	git := WorktreeGitFacts{CanonicalRoot: "/repo/feature", HeadObject: "abc123", PathAvailable: true}
	kent := WorktreeKentFacts{
		WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
		CanonicalRoot: "/repo/feature",
		DisplayName:   "feature",
	}
	valid := []WorktreeTopologyEntry{
		{Variant: WorktreeTopologyVariantRegistered, Registered: &WorktreeRegisteredFacts{Git: git, Kent: kent}},
		{Variant: WorktreeTopologyVariantExternal, External: &WorktreeExternalFacts{Git: git}},
		{Variant: WorktreeTopologyVariantMissing, Missing: &WorktreeMissingFacts{Kent: kent}},
	}
	for _, entry := range valid {
		if err := entry.Validate(); err != nil {
			t.Fatalf("%s entry rejected: %v", entry.Variant, err)
		}
	}

	invalid := []WorktreeTopologyEntry{
		{Variant: WorktreeTopologyVariantRegistered},
		{Variant: WorktreeTopologyVariantRegistered, Registered: &WorktreeRegisteredFacts{Git: git, Kent: kent}, External: &WorktreeExternalFacts{Git: git}},
		{Variant: WorktreeTopologyVariantExternal, Registered: &WorktreeRegisteredFacts{Git: git, Kent: kent}},
		{Variant: WorktreeTopologyVariantMissing, Missing: &WorktreeMissingFacts{}},
	}
	for _, entry := range invalid {
		if err := entry.Validate(); err == nil {
			t.Fatalf("invalid topology entry validated: %+v", entry)
		}
	}
	empty := ""
	if err := (WorktreeGitFacts{
		CanonicalRoot: "/repo/feature",
		HeadObject:    "abc123",
		BranchName:    &empty,
	}).Validate(); err == nil {
		t.Fatal("empty optional Git fact validated")
	}
	if err := (WorktreeKentFacts{
		WorktreeID:      kent.WorktreeID,
		CanonicalRoot:   kent.CanonicalRoot,
		DisplayName:     kent.DisplayName,
		OriginSessionID: &empty,
	}).Validate(); err == nil {
		t.Fatal("empty optional Kent fact validated")
	}
}

func TestWorktreeTopologyEntryDeletionSelectorCoversEveryDeletionVariant(t *testing.T) {
	registered := WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantRegistered,
		Registered: &WorktreeRegisteredFacts{
			Git: WorktreeGitFacts{
				CanonicalRoot: "/repo/feature",
				HeadObject:    "abc123",
				IsMain:        false,
			},
			Kent: WorktreeKentFacts{
				WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
				CanonicalRoot: "/repo/feature",
				DisplayName:   "feature",
			},
		},
	}
	missing := WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantMissing,
		Missing: &WorktreeMissingFacts{
			Kent: registered.Registered.Kent,
		},
	}
	external := WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantExternal,
		External: &WorktreeExternalFacts{
			Git: WorktreeGitFacts{
				CanonicalRoot: "/repo/external",
				HeadObject:    "def456",
				IsMain:        false,
			},
		},
	}
	mainRegistered := registered
	mainRegistered.Registered = &WorktreeRegisteredFacts{
		Git:  registered.Registered.Git,
		Kent: registered.Registered.Kent,
	}
	mainRegistered.Registered.Git.IsMain = true
	mainExternal := external
	mainExternal.External = &WorktreeExternalFacts{
		Git: external.External.Git,
	}
	mainExternal.External.Git.IsMain = true

	tests := []struct {
		name    string
		entry   WorktreeTopologyEntry
		want    string
		blocked bool
	}{
		{name: "registered", entry: registered, want: registered.Registered.Kent.WorktreeID},
		{name: "missing", entry: missing, want: missing.Missing.Kent.WorktreeID},
		{name: "external", entry: external, want: external.External.Git.CanonicalRoot},
		{name: "registered main", entry: mainRegistered, blocked: true},
		{name: "external main", entry: mainExternal, blocked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.entry.DeletionSelector()
			if test.blocked {
				if !errors.Is(err, ErrWorktreeBlocked) {
					t.Fatalf("DeletionSelector error = %v, want ErrWorktreeBlocked", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DeletionSelector: %v", err)
			}
			if got != test.want {
				t.Fatalf("DeletionSelector = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorktreeListEntryValidatesSessionActionProjection(t *testing.T) {
	enterSelector := "feature"
	deleteSelector := "worktree-id"
	branchName := "feature"
	registered := WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantRegistered,
		Registered: &WorktreeRegisteredFacts{
			Git: WorktreeGitFacts{
				CanonicalRoot: "/repo/feature",
				HeadObject:    "abc123",
				BranchName:    &branchName,
				PathAvailable: true,
			},
			Kent: WorktreeKentFacts{
				WorktreeID:    deleteSelector,
				CanonicalRoot: "/repo/feature",
				DisplayName:   "feature",
			},
		},
	}
	valid := WorktreeListEntry{
		Topology: registered,
		Projection: WorktreeListProjection{
			Selector: enterSelector,
			Switch: &WorktreeSwitchOperation{
				Kind:     WorktreeSwitchOperationEnter,
				Selector: &enterSelector,
			},
			DeletePreview: &WorktreeDeletePreviewOperation{Selector: deleteSelector},
		},
	}
	if err := valid.validateProjection(true); err != nil {
		t.Fatalf("valid session projection rejected: %v", err)
	}

	other := "other"
	tests := map[string]func(*WorktreeListEntry){
		"current row switches": func(entry *WorktreeListEntry) { entry.Projection.IsCurrent = true },
		"enter selector differs from row": func(entry *WorktreeListEntry) {
			entry.Projection.Switch = &WorktreeSwitchOperation{Kind: WorktreeSwitchOperationEnter, Selector: &other}
		},
		"registered row leaves main": func(entry *WorktreeListEntry) {
			entry.Projection.Switch = &WorktreeSwitchOperation{Kind: WorktreeSwitchOperationLeaveMain}
		},
		"delete selector differs from topology": func(entry *WorktreeListEntry) {
			entry.Projection.DeletePreview = &WorktreeDeletePreviewOperation{Selector: "/repo/feature"}
		},
		"registered row has external fallback": func(entry *WorktreeListEntry) {
			entry.Projection.FallbackIdentity = stringPointer("feature")
		},
		"main row has delete preview": func(entry *WorktreeListEntry) {
			git := registered.Registered.Git
			git.IsMain = true
			entry.Topology.Registered = &WorktreeRegisteredFacts{Git: git, Kent: registered.Registered.Kent}
			entry.Projection.Switch = &WorktreeSwitchOperation{Kind: WorktreeSwitchOperationLeaveMain}
		},
		"missing row switches": func(entry *WorktreeListEntry) {
			entry.Topology = WorktreeTopologyEntry{
				Variant: WorktreeTopologyVariantMissing,
				Missing: &WorktreeMissingFacts{Kent: registered.Registered.Kent},
			}
		},
		"unavailable path switches": func(entry *WorktreeListEntry) {
			git := registered.Registered.Git
			git.PathAvailable = false
			entry.Topology = WorktreeTopologyEntry{
				Variant:  WorktreeTopologyVariantExternal,
				External: &WorktreeExternalFacts{Git: git},
			}
			entry.Projection.DeletePreview = &WorktreeDeletePreviewOperation{Selector: git.CanonicalRoot}
		},
		"branch-backed external has fallback": func(entry *WorktreeListEntry) {
			entry.Topology = WorktreeTopologyEntry{
				Variant:  WorktreeTopologyVariantExternal,
				External: &WorktreeExternalFacts{Git: registered.Registered.Git},
			}
			entry.Projection.DeletePreview = &WorktreeDeletePreviewOperation{Selector: registered.Registered.Git.CanonicalRoot}
			entry.Projection.FallbackIdentity = stringPointer("external")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			entry := valid
			mutate(&entry)
			if err := entry.validateProjection(true); err == nil {
				t.Fatalf("invalid session projection validated: %+v", entry)
			}
		})
	}
}

func TestWorktreeProjectedResponsesValidateTheirProjectionScope(t *testing.T) {
	branchName := "feature"
	topology := WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantRegistered,
		Registered: &WorktreeRegisteredFacts{
			Git: WorktreeGitFacts{
				CanonicalRoot: "/repo/feature",
				HeadObject:    "abc123",
				BranchName:    &branchName,
				PathAvailable: true,
			},
			Kent: WorktreeKentFacts{
				WorktreeID:    "worktree-id",
				CanonicalRoot: "/repo/feature",
				DisplayName:   "feature",
			},
		},
	}
	sessionEntry, err := ProjectWorktreeListEntry(topology, branchName, false, true)
	if err != nil {
		t.Fatalf("ProjectWorktreeListEntry session: %v", err)
	}
	workspaceEntry, err := ProjectWorktreeListEntry(topology, branchName, false, false)
	if err != nil {
		t.Fatalf("ProjectWorktreeListEntry workspace: %v", err)
	}

	tests := []struct {
		response interface{ Validate() error }
		wantErr  bool
	}{
		{response: WorktreeListResponse{Worktrees: []WorktreeListEntry{sessionEntry}}},
		{response: WorktreeWorkspaceListResponse{WorkspaceID: "workspace", Worktrees: []WorktreeListEntry{workspaceEntry}}},
		{response: WorktreeSelectorPreviewResponse{Worktree: sessionEntry}},
		{response: WorktreeCreateResponse{Worktree: sessionEntry}},
		{response: WorktreeListResponse{Worktrees: []WorktreeListEntry{workspaceEntry}}, wantErr: true},
		{response: WorktreeWorkspaceListResponse{WorkspaceID: "workspace", Worktrees: []WorktreeListEntry{sessionEntry}}, wantErr: true},
		{response: WorktreeWorkspaceListResponse{Worktrees: []WorktreeListEntry{workspaceEntry}}, wantErr: true},
		{response: WorktreeSelectorPreviewResponse{Worktree: workspaceEntry}, wantErr: true},
		{response: WorktreeCreateResponse{Worktree: workspaceEntry}, wantErr: true},
	}
	for _, test := range tests {
		if err := test.response.Validate(); (err != nil) != test.wantErr {
			t.Fatalf("%T validation error = %v, wantErr %t", test.response, err, test.wantErr)
		}
	}
}

func TestWorktreeDeletePreviewResponseValidatesTopologyOwnedSelectorAndCleanliness(t *testing.T) {
	entry := WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantMissing,
		Missing: &WorktreeMissingFacts{
			Kent: WorktreeKentFacts{
				WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
				CanonicalRoot: "/repo/missing",
				DisplayName:   "missing",
			},
		},
	}
	valid := WorktreeDeletePreviewResponse{
		Worktree:         entry,
		DeletionSelector: entry.Missing.Kent.WorktreeID,
		Cleanliness:      clientui.WorktreeDirtyState{Kind: clientui.WorktreeDirtyStateClean},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid deletion preview rejected: %v", err)
	}
	mismatched := valid
	mismatched.DeletionSelector = "/repo/missing"
	if err := mismatched.Validate(); err == nil {
		t.Fatal("deletion preview accepted a selector that does not match topology")
	}
	notClean := valid
	notClean.Cleanliness = clientui.WorktreeDirtyState{
		Kind:         clientui.WorktreeDirtyStateUnknown,
		UnknownCause: stringPointer("status unavailable"),
	}
	if err := notClean.Validate(); err == nil {
		t.Fatal("missing deletion preview accepted non-cleanliness")
	}
}

func TestWorktreeStatusHasNoSelectorAndValidatesTypedProblems(t *testing.T) {
	response := WorktreeStatusResponse{
		Target: clientui.SessionExecutionTarget{
			WorkspaceID:   "workspace",
			WorkspaceRoot: "/repo",
			Worktree:      &clientui.SessionExecutionWorktreeTarget{ID: "wt", Root: "/repo/feature"},
		},
		Worktree: WorktreeStatusTarget{
			RecordedRoot: "/repo/feature",
			DisplayName:  stringPointer("feature"),
		},
		Problems: []WorktreeStatusProblem{{
			Kind: WorktreeStatusProblemRootMissing,
			Root: stringPointer("/repo/feature"),
		}},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("status response rejected: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if _, ok := fields["selector"]; ok {
		t.Fatal("status JSON exposed a selector")
	}
	var wire struct {
		Problems []map[string]json.RawMessage `json:"problems"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode status problems: %v", err)
	}
	if len(wire.Problems) != 1 {
		t.Fatalf("status problem count = %d, want 1", len(wire.Problems))
	}
	if _, ok := wire.Problems[0]["root"]; !ok {
		t.Fatalf("root problem omitted root: %+v", wire.Problems[0])
	}
	if _, ok := wire.Problems[0]["ref"]; ok {
		t.Fatalf("root problem exposed absent ref: %+v", wire.Problems[0])
	}
	for _, problem := range []WorktreeStatusProblem{
		{Kind: WorktreeStatusProblemRootInaccessible, Root: stringPointer("/repo/feature")},
		{Kind: WorktreeStatusProblemGitBindingMissing, Root: stringPointer("/repo/feature")},
		{Kind: WorktreeStatusProblemGitBindingMismatched, Root: stringPointer("/repo/feature")},
		{Kind: WorktreeStatusProblemRecordedRefMissing, Ref: stringPointer("refs/heads/feature")},
	} {
		if err := problem.Validate(); err != nil {
			t.Fatalf("status problem %s rejected: %v", problem.Kind, err)
		}
	}
	if err := (WorktreeStatusProblem{Kind: WorktreeStatusProblemRecordedRefMissing}).Validate(); err == nil {
		t.Fatal("recorded ref problem without ref validated")
	}
	if err := (WorktreeStatusProblem{
		Kind: WorktreeStatusProblemRootMissing,
		Root: stringPointer(""),
	}).Validate(); err == nil {
		t.Fatal("root problem with an empty root validated")
	}
	if err := (WorktreeStatusProblem{
		Kind: WorktreeStatusProblemRecordedRefMissing,
		Ref:  stringPointer("refs/heads/feature"),
		Root: stringPointer("/repo/feature"),
	}).Validate(); err == nil {
		t.Fatal("recorded ref problem with a root validated")
	}
}

func TestWorktreeWorkspaceListRequestRequiresProjectAndWorkspace(t *testing.T) {
	valid := WorktreeWorkspaceListRequest{ProjectID: "project-1", WorkspaceID: "workspace-1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid workspace list request rejected: %v", err)
	}
	for _, request := range []WorktreeWorkspaceListRequest{
		{WorkspaceID: "workspace-1"},
		{ProjectID: "project-1"},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid workspace list request validated: %+v", request)
		}
	}
}

func TestWorktreeTransitionRequestsUseUUIDV4Correlation(t *testing.T) {
	operationID := NewWorktreeOperationID()
	if err := operationID.Validate(); err != nil {
		t.Fatalf("generated operation id rejected: %v", err)
	}
	for _, raw := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "11111111-1111-1111-1111-111111111111"} {
		if _, err := ParseWorktreeOperationID(raw); err == nil {
			t.Fatalf("ParseWorktreeOperationID(%q) succeeded", raw)
		}
	}
	var decoded WorktreeScheduledAcknowledgement
	if err := json.Unmarshal([]byte(`{"operation_id":"11111111-1111-1111-1111-111111111111"}`), &decoded); err == nil {
		t.Fatal("scheduled acknowledgement accepted a non-v4 operation ID")
	}

	acknowledgement := WorktreeScheduledAcknowledgement{OperationID: operationID}
	if err := acknowledgement.Validate(); err != nil {
		t.Fatalf("scheduled acknowledgement rejected: %v", err)
	}
}

func TestWorktreeDeleteResultAndCleanupPoliciesAreDiscriminated(t *testing.T) {
	operationID := NewWorktreeOperationID()
	completed := WorktreeDeleteResult{
		Kind: WorktreeDeleteResultKindCompleted,
		Completed: &WorktreeDeleteCompletedResult{
			Cleanup: WorktreeBranchCleanupOutcome{
				Kind:       WorktreeBranchCleanupOutcomeDeleted,
				BranchName: stringPointer("feature"),
			},
		},
	}
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed deletion result rejected: %v", err)
	}
	scheduled := WorktreeDeleteResult{
		Kind:      WorktreeDeleteResultKindScheduled,
		Scheduled: &WorktreeScheduledAcknowledgement{OperationID: operationID},
	}
	if err := scheduled.Validate(); err != nil {
		t.Fatalf("scheduled deletion result rejected: %v", err)
	}
	if err := (WorktreeDeleteResult{
		Kind:      WorktreeDeleteResultKindScheduled,
		Completed: completed.Completed,
		Scheduled: scheduled.Scheduled,
	}).Validate(); err == nil {
		t.Fatal("delete result with both payloads validated")
	}
	for _, policy := range []WorktreeBranchCleanupMode{
		WorktreeBranchCleanupModeRetain,
		WorktreeBranchCleanupModeAutoIfKentCreated,
		WorktreeBranchCleanupModeDeleteSafe,
		WorktreeBranchCleanupModeDeleteForce,
	} {
		if err := policy.Validate(); err != nil {
			t.Fatalf("cleanup policy %q rejected: %v", policy, err)
		}
	}
	if err := (WorktreeBranchCleanupOutcome{
		Kind:       WorktreeBranchCleanupOutcomeRetained,
		BranchName: stringPointer("feature"),
	}).Validate(); err != nil {
		t.Fatalf("retained cleanup outcome rejected: %v", err)
	}
	if err := (WorktreeBranchCleanupOutcome{Kind: WorktreeBranchCleanupOutcomeDeleted}).Validate(); err == nil {
		t.Fatal("deleted cleanup outcome without branch validated")
	}
}

func TestWorktreeOperationRequestsRejectMissingRequiredFacts(t *testing.T) {
	operationID := NewWorktreeOperationID()
	valid := []interface{ Validate() error }{
		WorktreeSelectorPreviewRequest{SessionID: "session", Selector: "feature"},
		WorktreeEnterRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session"}, Selector: "feature"},
		WorktreeEnterRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session", Origin: &RuntimeStepOrigin{
			RunID: "018fdd67-89ab-4cde-8123-456789abc001", StepID: "018fdd67-89ab-4cde-8123-456789abc002",
		}}, Selector: "feature"},
		WorktreeLeaveRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session"}},
		WorktreeLeaveRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session", Origin: &RuntimeStepOrigin{
			RunID: "018fdd67-89ab-4cde-8123-456789abc001", StepID: "018fdd67-89ab-4cde-8123-456789abc002",
		}}},
		WorktreeDeleteRequest{
			WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session"},
			Selector:                 "feature",
			BranchCleanupPolicy:      WorktreeBranchCleanupModeRetain,
		},
		WorktreeDeleteRequest{
			WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session", Origin: &RuntimeStepOrigin{
				RunID: "018fdd67-89ab-4cde-8123-456789abc001", StepID: "018fdd67-89ab-4cde-8123-456789abc002",
			}},
			Selector:            "feature",
			BranchCleanupPolicy: WorktreeBranchCleanupModeRetain,
		},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Fatalf("%T rejected: %v", request, err)
		}
	}
	invalid := []interface{ Validate() error }{
		WorktreeSelectorPreviewRequest{SessionID: "session"},
		WorktreeEnterRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{SessionID: "session"}, Selector: "feature"},
		WorktreeEnterRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session", Origin: &RuntimeStepOrigin{}}, Selector: "feature"},
		WorktreeLeaveRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID}},
		WorktreeDeleteRequest{WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: operationID, SessionID: "session"}, Selector: "feature"},
	}
	for _, request := range invalid {
		if err := request.Validate(); err == nil {
			t.Fatalf("%T validated without required facts", request)
		}
	}
}

func TestWorktreeTransitionRequestsKeepFlatWireHeader(t *testing.T) {
	operationID := NewWorktreeOperationID()
	origin := &RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: "018fdd67-89ab-4cde-8123-456789abc002",
	}
	header := func(origin *RuntimeStepOrigin) WorktreeTransitionHeader {
		return WorktreeTransitionHeader{OperationID: operationID, SessionID: "session", Origin: origin}
	}
	for _, testCase := range []struct {
		name         string
		request      any
		decodeHeader func(*testing.T, []byte) WorktreeTransitionHeader
		wantOrigin   bool
	}{
		{
			name:    "enter external",
			request: WorktreeEnterRequest{WorktreeTransitionHeader: header(nil), Selector: "feature"},
			decodeHeader: func(t *testing.T, data []byte) WorktreeTransitionHeader {
				t.Helper()
				var request WorktreeEnterRequest
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode enter request: %v", err)
				}
				return request.WorktreeTransitionHeader
			},
		},
		{
			name:    "enter model",
			request: WorktreeEnterRequest{WorktreeTransitionHeader: header(origin), Selector: "feature"},
			decodeHeader: func(t *testing.T, data []byte) WorktreeTransitionHeader {
				t.Helper()
				var request WorktreeEnterRequest
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode enter request: %v", err)
				}
				return request.WorktreeTransitionHeader
			},
			wantOrigin: true,
		},
		{
			name:    "leave external",
			request: WorktreeLeaveRequest{WorktreeTransitionHeader: header(nil)},
			decodeHeader: func(t *testing.T, data []byte) WorktreeTransitionHeader {
				t.Helper()
				var request WorktreeLeaveRequest
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode leave request: %v", err)
				}
				return request.WorktreeTransitionHeader
			},
		},
		{
			name:    "leave model",
			request: WorktreeLeaveRequest{WorktreeTransitionHeader: header(origin)},
			decodeHeader: func(t *testing.T, data []byte) WorktreeTransitionHeader {
				t.Helper()
				var request WorktreeLeaveRequest
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode leave request: %v", err)
				}
				return request.WorktreeTransitionHeader
			},
			wantOrigin: true,
		},
		{
			name: "delete external",
			request: WorktreeDeleteRequest{
				WorktreeTransitionHeader: header(nil),
				Selector:                 "feature",
				BranchCleanupPolicy:      WorktreeBranchCleanupModeRetain,
			},
			decodeHeader: func(t *testing.T, data []byte) WorktreeTransitionHeader {
				t.Helper()
				var request WorktreeDeleteRequest
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode delete request: %v", err)
				}
				return request.WorktreeTransitionHeader
			},
		},
		{
			name: "delete model",
			request: WorktreeDeleteRequest{
				WorktreeTransitionHeader: header(origin),
				Selector:                 "feature",
				BranchCleanupPolicy:      WorktreeBranchCleanupModeRetain,
			},
			decodeHeader: func(t *testing.T, data []byte) WorktreeTransitionHeader {
				t.Helper()
				var request WorktreeDeleteRequest
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode delete request: %v", err)
				}
				return request.WorktreeTransitionHeader
			},
			wantOrigin: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := json.Marshal(testCase.request)
			if err != nil {
				t.Fatalf("encode request: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("decode request fields: %v", err)
			}
			if _, present := fields["worktree_transition_header"]; present {
				t.Fatalf("wire payload nested the transition header: %s", data)
			}
			for _, name := range []string{"operation_id", "session_id"} {
				if _, present := fields[name]; !present {
					t.Fatalf("wire payload omitted %s: %s", name, data)
				}
			}
			_, hasOrigin := fields["origin"]
			if hasOrigin != testCase.wantOrigin {
				t.Fatalf("wire origin present=%t, want %t: %s", hasOrigin, testCase.wantOrigin, data)
			}
			decoded := testCase.decodeHeader(t, data)
			if decoded.OperationID != operationID || decoded.SessionID != "session" {
				t.Fatalf("decoded header=%+v", decoded)
			}
			if testCase.wantOrigin {
				if decoded.Origin == nil || *decoded.Origin != *origin {
					t.Fatalf("decoded origin=%+v, want %+v", decoded.Origin, origin)
				}
			} else if decoded.Origin != nil {
				t.Fatalf("decoded external origin=%+v", decoded.Origin)
			}
		})
	}
}

func TestWorktreeCreateWireUsesOnlySetupOperationIdentity(t *testing.T) {
	request := WorktreeCreateRequest{
		SetupOperationID: NewWorktreeSetupOperationID(),
		SessionID:        "session",
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature",
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, exists := fields["client_request_id"]; exists {
		t.Fatalf("Worktree Create retained generic client request identity: %s", encoded)
	}
	if _, exists := fields["setup_operation_id"]; !exists {
		t.Fatalf("Worktree Create omitted setup operation identity: %s", encoded)
	}
}

func stringPointer(value string) *string {
	return &value
}
