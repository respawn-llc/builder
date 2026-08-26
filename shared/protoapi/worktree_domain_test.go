package protoapi

import (
	"errors"
	"reflect"
	"testing"

	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/serverapi"
	"core/shared/workflowcontract"
	"core/shared/worktreecontract"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestWorktreeProtoMapper(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "status request", run: func(t *testing.T) {
			domain := worktreecontract.StatusRequest{SessionID: "session-1"}
			message, err := WorktreeStatusRequestToProto(domain)
			if err != nil {
				t.Fatalf("WorktreeStatusRequestToProto: %v", err)
			}
			want := &worktreepb.StatusRequest{SessionId: domain.SessionID}
			if !proto.Equal(message, want) {
				t.Fatalf("WorktreeStatusRequestToProto() = %#v, want %#v", message, want)
			}
			roundTrip, err := WorktreeStatusRequestFromProto(message)
			if err != nil {
				t.Fatalf("WorktreeStatusRequestFromProto: %v", err)
			}
			if !reflect.DeepEqual(roundTrip, domain) {
				t.Fatalf("WorktreeStatusRequestFromProto() = %#v, want %#v", roundTrip, domain)
			}
		}},

		{name: "list request", run: func(t *testing.T) {
			domain := worktreecontract.ListRequest{SessionID: "session-2"}
			message, err := WorktreeListRequestToProto(domain)
			if err != nil {
				t.Fatalf("WorktreeListRequestToProto: %v", err)
			}
			want := &worktreepb.ListRequest{SessionId: domain.SessionID}
			if !proto.Equal(message, want) {
				t.Fatalf("WorktreeListRequestToProto() = %v, want %v", message, want)
			}
			roundTrip, err := WorktreeListRequestFromProto(message)
			if err != nil {
				t.Fatalf("WorktreeListRequestFromProto: %v", err)
			}
			if !reflect.DeepEqual(roundTrip, domain) {
				t.Fatalf("WorktreeListRequestFromProto() = %#v, want %#v", roundTrip, domain)
			}
		}},

		{name: "workspace list request", run: func(t *testing.T) {
			domain := worktreecontract.WorkspaceListRequest{ProjectID: "project-1", WorkspaceID: "workspace-1"}
			message, err := WorktreeWorkspaceListRequestToProto(domain)
			if err != nil {
				t.Fatalf("WorktreeWorkspaceListRequestToProto: %v", err)
			}
			want := &worktreepb.WorkspaceListRequest{ProjectId: domain.ProjectID, WorkspaceId: domain.WorkspaceID}
			if !proto.Equal(message, want) {
				t.Fatalf("WorktreeWorkspaceListRequestToProto() = %v, want %v", message, want)
			}
			roundTrip, err := WorktreeWorkspaceListRequestFromProto(message)
			if err != nil {
				t.Fatalf("WorktreeWorkspaceListRequestFromProto: %v", err)
			}
			if !reflect.DeepEqual(roundTrip, domain) {
				t.Fatalf("WorktreeWorkspaceListRequestFromProto() = %#v, want %#v", roundTrip, domain)
			}
		}},

		{name: "status success", run: func(t *testing.T) {
			root := "/repo"
			ref := "refs/heads/main"
			domain := worktreecontract.StatusResponse{
				Target: worktreecontract.SessionExecutionTarget{
					WorkspaceID:           "workspace-1",
					WorkspaceName:         "Workspace",
					WorkspaceRoot:         root,
					WorkspaceAvailability: worktreecontract.ProjectAvailabilityAvailable,
					Worktree: &worktreecontract.SessionExecutionWorktreeTarget{
						ID: "worktree-1", Name: "main", Root: root, Availability: "available",
					},
					EffectiveWorkdir: root,
				},
				Worktree: worktreecontract.StatusTarget{
					RecordedRoot: root, ObservedRoot: &root, RecordedBranchRef: &ref, ObservedBranchRef: &ref,
				},
				Problems: []worktreecontract.StatusProblem{
					{Kind: worktreecontract.StatusProblemRootMissing, Root: &root},
					{Kind: worktreecontract.StatusProblemRootInaccessible, Root: &root},
					{Kind: worktreecontract.StatusProblemGitBindingMissing, Root: &root},
					{Kind: worktreecontract.StatusProblemGitBindingMismatched, Root: &root},
					{Kind: worktreecontract.StatusProblemRecordedRefMissing, Ref: &ref},
				},
			}
			message, err := WorktreeStatusSuccessToProto(domain)
			if err != nil {
				t.Fatalf("WorktreeStatusSuccessToProto: %v", err)
			}
			roundTrip, err := WorktreeStatusSuccessFromProto(message)
			if err != nil {
				t.Fatalf("WorktreeStatusSuccessFromProto: %v", err)
			}
			if !reflect.DeepEqual(roundTrip, domain) {
				t.Fatalf("WorktreeStatusSuccessFromProto() = %#v, want %#v", roundTrip, domain)
			}
		}},

		{name: "registered list success", run: func(t *testing.T) {
			root := "/repo"
			domain := worktreecontract.ListResponse{
				Target: worktreecontract.SessionExecutionTarget{
					WorkspaceID:           "workspace-1",
					WorkspaceRoot:         root,
					WorkspaceAvailability: worktreecontract.ProjectAvailabilityAvailable,
					EffectiveWorkdir:      root,
				},
				Worktrees: []worktreecontract.ListEntry{worktreeMapperRegisteredEntry(root)},
			}
			message, err := WorktreeListSuccessToProto(domain)
			if err != nil {
				t.Fatalf("WorktreeListSuccessToProto: %v", err)
			}
			roundTrip, err := WorktreeListSuccessFromProto(message)
			if err != nil {
				t.Fatalf("WorktreeListSuccessFromProto: %v", err)
			}
			if !reflect.DeepEqual(roundTrip, domain) {
				t.Fatalf("WorktreeListSuccessFromProto() = %#v, want %#v", roundTrip, domain)
			}
		}},

		{name: "remaining unary successes", run: func(t *testing.T) {
			root := "/repo"
			entry := worktreeMapperRegisteredEntry(root)
			target := worktreecontract.SessionExecutionTarget{
				WorkspaceID:           "workspace-1",
				WorkspaceRoot:         root,
				WorkspaceAvailability: worktreecontract.ProjectAvailabilityAvailable,
				EffectiveWorkdir:      root,
			}

			workspaceList := worktreecontract.WorkspaceListResponse{
				WorkspaceID: "workspace-1",
				Worktrees:   []worktreecontract.ListEntry{worktreeMapperWorkspaceEntry(root)},
			}
			workspaceListMessage, err := WorktreeWorkspaceListSuccessToProto(workspaceList)
			if err != nil {
				t.Fatal(err)
			}
			workspaceListRoundTrip, err := WorktreeWorkspaceListSuccessFromProto(workspaceListMessage)
			if err != nil || !reflect.DeepEqual(workspaceListRoundTrip, workspaceList) {
				t.Fatalf("workspace list round trip = %#v, %v", workspaceListRoundTrip, err)
			}

			selector := worktreecontract.SelectorResolveResponse{Worktree: entry}
			selectorMessage, err := WorktreeSelectorResolveSuccessToProto(selector)
			if err != nil {
				t.Fatal(err)
			}
			selectorRoundTrip, err := WorktreeSelectorResolveSuccessFromProto(selectorMessage)
			if err != nil || !reflect.DeepEqual(selectorRoundTrip, selector) {
				t.Fatalf("selector success round trip = %#v, %v", selectorRoundTrip, err)
			}

			missing := worktreecontract.TopologyEntry{
				Variant: worktreecontract.TopologyVariantMissing,
				Missing: &worktreecontract.MissingFacts{Kent: worktreecontract.KentFacts{
					WorktreeID: "missing-1", CanonicalRoot: "/missing", DisplayName: "missing", Managed: true,
				}},
			}
			preview := worktreecontract.DeletePreviewResponse{
				Worktree: missing, DeletionSelector: "missing-1",
				Cleanliness: worktreecontract.DirtyState{Kind: worktreecontract.DirtyStateClean},
			}
			previewMessage, err := WorktreeDeletePreviewSuccessToProto(preview)
			if err != nil {
				t.Fatal(err)
			}
			previewRoundTrip, err := WorktreeDeletePreviewSuccessFromProto(previewMessage)
			if err != nil || !reflect.DeepEqual(previewRoundTrip, preview) {
				t.Fatalf("preview success round trip = %#v, %v", previewRoundTrip, err)
			}

			resolution := worktreecontract.CreateTargetResolveResponse{
				Resolution: worktreecontract.CreateTargetResolution{
					Input: "feature", Kind: worktreecontract.CreateTargetResolutionKindExistingBranch,
					ResolvedRef: "refs/heads/feature",
				},
			}
			resolutionMessage, err := WorktreeCreateTargetResolveSuccessToProto(resolution)
			if err != nil {
				t.Fatal(err)
			}
			resolutionRoundTrip, err := WorktreeCreateTargetResolveSuccessFromProto(resolutionMessage)
			if err != nil || !reflect.DeepEqual(resolutionRoundTrip, resolution) {
				t.Fatalf("create target success round trip = %#v, %v", resolutionRoundTrip, err)
			}

			create := worktreecontract.CreateResponse{Target: target, Worktree: entry}
			createMessage, err := WorktreeCreateSuccessToProto(create)
			if err != nil {
				t.Fatal(err)
			}
			createRoundTrip, err := WorktreeCreateSuccessFromProto(createMessage)
			if err != nil || !reflect.DeepEqual(createRoundTrip, create) {
				t.Fatalf("create success round trip = %#v, %v", createRoundTrip, err)
			}

			operationID, err := worktreecontract.ParseOperationID("123e4567-e89b-42d3-a456-426614174000")
			if err != nil {
				t.Fatal(err)
			}
			ack := worktreecontract.ScheduledAcknowledgement{OperationID: operationID}
			ackMessage, err := WorktreeScheduledAcknowledgementToProto(ack)
			if err != nil {
				t.Fatal(err)
			}
			ackRoundTrip, err := WorktreeScheduledAcknowledgementFromProto(ackMessage)
			if err != nil || !reflect.DeepEqual(ackRoundTrip, ack) {
				t.Fatalf("ack round trip = %#v, %v", ackRoundTrip, err)
			}

			branch := "feature"
			completed := worktreecontract.DeleteResult{
				Kind: worktreecontract.DeleteResultKindCompleted,
				Completed: &worktreecontract.DeleteCompletedResult{
					Cleanup: worktreecontract.BranchCleanupOutcome{
						Kind: worktreecontract.BranchCleanupOutcomeDeleted, BranchName: &branch,
					},
				},
			}
			completedMessage, err := WorktreeDeleteSuccessToProto(completed)
			if err != nil {
				t.Fatal(err)
			}
			completedRoundTrip, err := WorktreeDeleteSuccessFromProto(completedMessage)
			if err != nil || !reflect.DeepEqual(completedRoundTrip, completed) {
				t.Fatalf("completed delete round trip = %#v, %v", completedRoundTrip, err)
			}

			scheduled := worktreecontract.DeleteResult{
				Kind: worktreecontract.DeleteResultKindScheduled, Scheduled: &ack,
			}
			scheduledMessage, err := WorktreeDeleteSuccessToProto(scheduled)
			if err != nil {
				t.Fatal(err)
			}
			scheduledRoundTrip, err := WorktreeDeleteSuccessFromProto(scheduledMessage)
			if err != nil || !reflect.DeepEqual(scheduledRoundTrip, scheduled) {
				t.Fatalf("scheduled delete round trip = %#v, %v", scheduledRoundTrip, err)
			}
		}},

		{name: "remaining unary requests", run: func(t *testing.T) {
			operationID, err := worktreecontract.ParseOperationID("123e4567-e89b-42d3-a456-426614174000")
			if err != nil {
				t.Fatal(err)
			}
			setupOperationID, err := worktreecontract.ParseSetupOperationID("223e4567-e89b-42d3-a456-426614174000")
			if err != nil {
				t.Fatal(err)
			}
			header := worktreecontract.TransitionHeader{
				OperationID: operationID,
				SessionID:   "session-1",
				Origin: &worktreecontract.RuntimeStepOrigin{
					RunID: "323e4567-e89b-42d3-a456-426614174000", StepID: "423e4567-e89b-42d3-a456-426614174000",
				},
			}

			selector := worktreecontract.SelectorResolveRequest{SessionID: "session-1", Selector: "feature"}
			selectorMessage, err := WorktreeSelectorResolveRequestToProto(selector)
			if err != nil {
				t.Fatal(err)
			}
			selectorRoundTrip, err := WorktreeSelectorResolveRequestFromProto(selectorMessage)
			if err != nil || !reflect.DeepEqual(selectorRoundTrip, selector) {
				t.Fatalf("selector round trip = %#v, %v", selectorRoundTrip, err)
			}

			preview := worktreecontract.DeletePreviewRequest{SessionID: "session-1", Selector: "feature"}
			previewMessage, err := WorktreeDeletePreviewRequestToProto(preview)
			if err != nil {
				t.Fatal(err)
			}
			previewRoundTrip, err := WorktreeDeletePreviewRequestFromProto(previewMessage)
			if err != nil || !reflect.DeepEqual(previewRoundTrip, preview) {
				t.Fatalf("preview round trip = %#v, %v", previewRoundTrip, err)
			}

			target := worktreecontract.CreateTargetResolveRequest{SessionID: "session-1", Target: "feature"}
			targetMessage, err := WorktreeCreateTargetResolveRequestToProto(target)
			if err != nil {
				t.Fatal(err)
			}
			targetRoundTrip, err := WorktreeCreateTargetResolveRequestFromProto(targetMessage)
			if err != nil || !reflect.DeepEqual(targetRoundTrip, target) {
				t.Fatalf("target round trip = %#v, %v", targetRoundTrip, err)
			}

			create := worktreecontract.CreateRequest{
				SetupOperationID: setupOperationID,
				SessionID:        "session-1",
				BaseRef:          "main",
				CreateBranch:     true,
				BranchName:       "feature",
				RootPath:         "/repo-feature",
			}
			createMessage, err := WorktreeCreateRequestToProto(create)
			if err != nil {
				t.Fatal(err)
			}
			createRoundTrip, err := WorktreeCreateRequestFromProto(createMessage)
			if err != nil || !reflect.DeepEqual(createRoundTrip, create) {
				t.Fatalf("create round trip = %#v, %v", createRoundTrip, err)
			}
			existing := worktreecontract.CreateRequest{
				SetupOperationID: setupOperationID,
				SessionID:        "session-1",
				BaseRef:          "refs/heads/existing",
			}
			existingMessage, err := WorktreeCreateRequestToProto(existing)
			if err != nil {
				t.Fatal(err)
			}
			existingRoundTrip, err := WorktreeCreateRequestFromProto(existingMessage)
			if err != nil || !reflect.DeepEqual(existingRoundTrip, existing) {
				t.Fatalf("existing create round trip = %#v, %v", existingRoundTrip, err)
			}

			enter := worktreecontract.EnterRequest{TransitionHeader: header, Selector: "feature"}
			enterMessage, err := WorktreeEnterRequestToProto(enter)
			if err != nil {
				t.Fatal(err)
			}
			enterRoundTrip, err := WorktreeEnterRequestFromProto(enterMessage)
			if err != nil || !reflect.DeepEqual(enterRoundTrip, enter) {
				t.Fatalf("enter round trip = %#v, %v", enterRoundTrip, err)
			}

			leave := worktreecontract.LeaveRequest{TransitionHeader: header}
			leaveMessage, err := WorktreeLeaveRequestToProto(leave)
			if err != nil {
				t.Fatal(err)
			}
			leaveRoundTrip, err := WorktreeLeaveRequestFromProto(leaveMessage)
			if err != nil || !reflect.DeepEqual(leaveRoundTrip, leave) {
				t.Fatalf("leave round trip = %#v, %v", leaveRoundTrip, err)
			}

			deleteRequest := worktreecontract.DeleteRequest{
				TransitionHeader:    header,
				Selector:            "feature",
				ForceFolderRemoval:  true,
				BranchCleanupPolicy: worktreecontract.BranchCleanupModeDeleteSafe,
			}
			deleteMessage, err := WorktreeDeleteRequestToProto(deleteRequest)
			if err != nil {
				t.Fatal(err)
			}
			deleteRoundTrip, err := WorktreeDeleteRequestFromProto(deleteMessage)
			if err != nil || !reflect.DeepEqual(deleteRoundTrip, deleteRequest) {
				t.Fatalf("delete round trip = %#v, %v", deleteRoundTrip, err)
			}

			setup := worktreecontract.SetupSubscribeRequest{SetupOperationID: setupOperationID}
			setupMessage, err := WorktreeSetupSubscribeRequestToProto(setup)
			if err != nil {
				t.Fatal(err)
			}
			setupRoundTrip, err := WorktreeSetupSubscribeRequestFromProto(setupMessage)
			if err != nil || !reflect.DeepEqual(setupRoundTrip, setup) {
				t.Fatalf("setup subscribe round trip = %#v, %v", setupRoundTrip, err)
			}
		}},

		{name: "declared errors", run: func(t *testing.T) {
			operationID, err := worktreecontract.ParseOperationID("123e4567-e89b-42d3-a456-426614174000")
			if err != nil {
				t.Fatal(err)
			}
			root := "/repo"
			registered := worktreeMapperRegisteredEntry(root).Topology
			retained, err := worktreecontract.NewSetupRetainedError(registered, "/setup.sh", "setup failed", nil)
			if err != nil {
				t.Fatal(err)
			}
			retained.RetainedPreviousWorktree = &worktreecontract.RetainedPreviousWorktree{Worktree: registered}
			if err := retained.Validate(); err != nil {
				t.Fatal(err)
			}
			dirtyCount := 2
			unknownCause := "git status failed"
			ambiguous := &worktreecontract.SelectorError{
				Kind:  worktreecontract.SelectorErrorKindAmbiguous,
				Input: "feature",
				Candidates: []worktreecontract.SelectorCandidate{
					{
						Variant: worktreecontract.TopologyVariantRegistered, Selector: "worktree-1",
						FallbackIdentity: "worktree-1",
					},
					{
						Variant: worktreecontract.TopologyVariantExternal, Selector: "/external",
						FallbackIdentity: "/external",
					},
					{
						Variant: worktreecontract.TopologyVariantMissing, Selector: "missing-1",
						FallbackIdentity: "missing-1",
					},
				},
			}
			workspaceDetails := &projectpb.WorkspaceNotRegisteredDetails{}
			projectID := "project-1"
			workspaceID := "workspace-1"
			workspaceDetails.ProjectId = &projectID
			workspaceDetails.WorkspaceId = &workspaceID
			readiness := serverapi.NewServerNotReadyError(
				serverapi.ServerNotReadyActivationFailed,
				serverapi.ServerNotReadyDetails{OnboardingCompleted: true},
				nil,
			)
			statusMethod := worktreepb.File_kent_api_worktree_worktree_proto.Services().
				ByName("StatusService").Methods().ByName("Get")
			workspaceListMethod := worktreepb.File_kent_api_worktree_worktree_proto.Services().
				ByName("ListService").Methods().ByName("ListWorkspace")
			tests := []struct {
				name       string
				input      error
				workspace  *projectpb.WorkspaceNotRegisteredDetails
				wantTarget error
				method     protoreflect.MethodDescriptor
			}{
				{name: "auth required", input: serverapi.ErrServerAuthRequired, wantTarget: serverapi.ErrServerAuthRequired, method: statusMethod},
				{name: "server not ready", input: readiness, wantTarget: serverapi.ErrServerNotReadyActivationFailed, method: statusMethod},
				{name: "workspace not registered", input: serverapi.ErrWorkspaceNotRegistered, workspace: workspaceDetails, wantTarget: serverapi.ErrWorkspaceNotRegistered, method: workspaceListMethod},
				{name: "selector not found", input: &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindNotFound, Input: "missing"}, wantTarget: worktreecontract.ErrWorktreeSelectorNotFound},
				{name: "selector ambiguous", input: ambiguous, wantTarget: worktreecontract.ErrWorktreeSelectorAmbiguous},
				{name: "selector unavailable", input: &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindUnavailable, Input: "missing"}, wantTarget: worktreecontract.ErrWorktreeSelectorUnavailable},
				{name: "blocked", input: errors.Join(worktreecontract.ErrWorktreeBlocked, errors.New("background process active")), wantTarget: worktreecontract.ErrWorktreeBlocked},
				{name: "transition pending", input: &worktreecontract.TransitionPendingError{SessionID: "session-1", PendingOperationID: operationID}, wantTarget: worktreecontract.ErrWorktreeTransitionPending},
				{name: "immediate origin inactive", input: worktreecontract.NewImmediateTransitionError(worktreecontract.ImmediateTransitionOriginInactive, errors.New("origin inactive"))},
				{name: "immediate apply failed", input: worktreecontract.NewImmediateTransitionError(worktreecontract.ImmediateTransitionApplyFailed, errors.New("apply failed"))},
				{name: "create base ref", input: worktreecontract.NewCreateError(worktreecontract.CreateErrorOwnerBaseRef, "base ref invalid", nil)},
				{name: "create form", input: worktreecontract.NewCreateError(worktreecontract.CreateErrorOwnerForm, "form invalid", nil)},
				{name: "setup retained", input: retained, wantTarget: worktreecontract.ErrWorktreeSetupRetained},
				{name: "delete dirty", input: &worktreecontract.DeletePreconditionError{DirtyState: worktreecontract.DirtyState{Kind: worktreecontract.DirtyStateDirty, DirtyFileCount: &dirtyCount}}, wantTarget: worktreecontract.ErrWorktreeDeletePrecondition},
				{name: "delete unknown", input: &worktreecontract.DeletePreconditionError{DirtyState: worktreecontract.DirtyState{Kind: worktreecontract.DirtyStateUnknown, UnknownCause: &unknownCause}}, wantTarget: worktreecontract.ErrWorktreeDeletePrecondition},
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					detail, known, conversionErr := WorktreeErrorToProto(test.input, test.workspace)
					if conversionErr != nil || !known {
						t.Fatalf("WorktreeErrorToProto() = %T, %t, %v", detail, known, conversionErr)
					}
					roundTrip, conversionErr := WorktreeErrorFromProto(detail)
					if conversionErr != nil {
						t.Fatalf("WorktreeErrorFromProto: %v", conversionErr)
					}
					if test.wantTarget != nil && !errors.Is(roundTrip, test.wantTarget) {
						t.Fatalf("round trip error = %v, want category %v", roundTrip, test.wantTarget)
					}
					if roundTrip.Error() != test.input.Error() {
						t.Fatalf("round trip diagnostic = %q, want %q", roundTrip.Error(), test.input.Error())
					}
					if test.method != nil {
						result, resultErr := FailureResult(test.method, detail)
						if resultErr != nil {
							t.Fatalf("FailureResult: %v", resultErr)
						}
						classified, resultErr := ClassifyResult(result)
						if resultErr != nil {
							t.Fatalf("ClassifyResult: %v", resultErr)
						}
						if classified.Outcome != OperationKnownFailure ||
							classified.Failure == nil ||
							!proto.Equal(classified.Failure.Detail.Interface(), detail) {
							t.Fatalf("classified failure = %#v, want detail %T", classified, detail)
						}
					}
				})
			}

			internal := &sharedpb.InternalFailureDetails{}
			cause := "unexpected failure"
			internal.Cause = &cause
			roundTrip, err := WorktreeErrorFromProto(internal)
			if err != nil || roundTrip.Error() != cause {
				t.Fatalf("internal failure round trip = %v, %v", roundTrip, err)
			}
			if detail, known, err := WorktreeErrorToProto(errors.New("unexpected"), nil); err != nil || known || detail != nil {
				t.Fatalf("unclassified failure = %T, %t, %v", detail, known, err)
			}
		}},

		{name: "remaining enum branches", run: func(t *testing.T) {
			for _, availability := range []worktreecontract.ProjectAvailability{
				worktreecontract.ProjectAvailabilityAvailable,
				worktreecontract.ProjectAvailabilityMissing,
				worktreecontract.ProjectAvailabilityInaccessible,
				worktreecontract.ProjectAvailabilityUnlinked,
			} {
				message, err := worktreeProjectAvailabilityToProto(availability)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, err := worktreeProjectAvailabilityFromProto(message)
				if err != nil || roundTrip != availability {
					t.Fatalf("availability round trip = %q, %v; want %q", roundTrip, err, availability)
				}
			}

			root := "/external"
			branch := "feature"
			external := worktreecontract.TopologyEntry{
				Variant: worktreecontract.TopologyVariantExternal,
				External: &worktreecontract.ExternalFacts{Git: worktreecontract.GitFacts{
					CanonicalRoot: root, HeadObject: "def", BranchName: &branch, PathAvailable: true,
				}},
			}
			externalMessage, err := WorktreeTopologyEntryToProto(external)
			if err != nil {
				t.Fatal(err)
			}
			externalRoundTrip, err := WorktreeTopologyEntryFromProto(externalMessage)
			if err != nil || !reflect.DeepEqual(externalRoundTrip, external) {
				t.Fatalf("external topology round trip = %#v, %v", externalRoundTrip, err)
			}

			selector := "feature"
			for _, operation := range []worktreecontract.SwitchOperation{
				{Kind: worktreecontract.SwitchOperationEnter, Selector: &selector},
				{Kind: worktreecontract.SwitchOperationLeaveMain},
			} {
				projection := worktreecontract.ListProjection{Selector: selector, Switch: &operation}
				message, err := worktreeListProjectionToProto(projection)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, err := worktreeListProjectionFromProto(message)
				if err != nil || !reflect.DeepEqual(roundTrip, projection) {
					t.Fatalf("switch operation round trip = %#v, %v", roundTrip, err)
				}
			}
			fallback := "external"
			projection := worktreecontract.ListProjection{
				Selector: "external",
				DeletePreview: &worktreecontract.DeletePreviewOperation{
					Selector: "/external",
				},
				FallbackIdentity: &fallback,
			}
			projectionMessage, err := worktreeListProjectionToProto(projection)
			if err != nil {
				t.Fatal(err)
			}
			projectionRoundTrip, err := worktreeListProjectionFromProto(projectionMessage)
			if err != nil || !reflect.DeepEqual(projectionRoundTrip, projection) {
				t.Fatalf("projection round trip = %#v, %v", projectionRoundTrip, err)
			}

			retainedPrevious, err := worktreeRetainedPreviousToProto(nil)
			if err != nil || retainedPrevious != nil {
				t.Fatalf("absent retained previous = %v, %v", retainedPrevious, err)
			}

			for _, mode := range []worktreecontract.BranchCleanupMode{
				worktreecontract.BranchCleanupModeRetain,
				worktreecontract.BranchCleanupModeAutoIfKentCreated,
				worktreecontract.BranchCleanupModeDeleteSafe,
				worktreecontract.BranchCleanupModeDeleteForce,
			} {
				message, err := worktreeBranchCleanupModeToProto(mode)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, err := worktreeBranchCleanupModeFromProto(message)
				if err != nil || roundTrip != mode {
					t.Fatalf("cleanup mode round trip = %q, %v; want %q", roundTrip, err, mode)
				}
			}

			diagnostic := "branch retained"
			for _, outcome := range []worktreecontract.BranchCleanupOutcome{
				{Kind: worktreecontract.BranchCleanupOutcomeNotRequested},
				{Kind: worktreecontract.BranchCleanupOutcomeNotApplicable},
				{Kind: worktreecontract.BranchCleanupOutcomeDeleted, BranchName: &branch},
				{Kind: worktreecontract.BranchCleanupOutcomeRetained, BranchName: &branch, Diagnostic: &diagnostic},
			} {
				message, err := worktreeBranchCleanupOutcomeToProto(outcome)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, err := worktreeBranchCleanupOutcomeFromProto(message)
				if err != nil || !reflect.DeepEqual(roundTrip, outcome) {
					t.Fatalf("cleanup outcome round trip = %#v, %v", roundTrip, err)
				}
			}

			for _, resolution := range []worktreecontract.CreateTargetResolution{
				{Input: "new", Kind: worktreecontract.CreateTargetResolutionKindNewBranch},
				{Input: "existing", Kind: worktreecontract.CreateTargetResolutionKindExistingBranch, ResolvedRef: "refs/heads/existing"},
				{Input: "commit", Kind: worktreecontract.CreateTargetResolutionKindDetachedRef, ResolvedRef: "abc123"},
			} {
				message, err := worktreeCreateTargetResolutionToProto(resolution)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip, err := worktreeCreateTargetResolutionFromProto(message)
				if err != nil || !reflect.DeepEqual(roundTrip, resolution) {
					t.Fatalf("create target round trip = %#v, %v", roundTrip, err)
				}
			}

			if err := Validate(&worktreepb.BlockedDetails{}); err == nil {
				t.Fatal("BlockedDetails without diagnostic unexpectedly validated")
			}
			if err := Validate(&worktreepb.ImmediateTransitionDetails{
				Kind: worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_ORIGIN_INACTIVE,
			}); err == nil {
				t.Fatal("ImmediateTransitionDetails without diagnostic unexpectedly validated")
			}
		}},
		{name: "setup events", run: func(t *testing.T) {
			for _, event := range worktreeMapperSetupEvents(t) {
				message, err := WorktreeSetupEventToProto(event)
				if err != nil {
					t.Fatalf("WorktreeSetupEventToProto(%s): %v", event.Phase, err)
				}
				roundTrip, err := WorktreeSetupEventFromProto(message)
				if err != nil {
					t.Fatalf("WorktreeSetupEventFromProto(%s): %v", event.Phase, err)
				}
				if !reflect.DeepEqual(roundTrip, event) {
					t.Fatalf("setup event round trip = %#v, want %#v", roundTrip, event)
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func worktreeMapperRegisteredEntry(root string) worktreecontract.ListEntry {
	branchRef := "refs/heads/main"
	branchName := "main"
	selector := "worktree-1"
	return worktreecontract.ListEntry{
		Topology: worktreecontract.TopologyEntry{
			Variant: worktreecontract.TopologyVariantRegistered,
			Registered: &worktreecontract.RegisteredFacts{
				Git: worktreecontract.GitFacts{
					CanonicalRoot: root,
					HeadObject:    "abc",
					BranchRef:     &branchRef,
					BranchName:    &branchName,
					IsMain:        true,
					PathAvailable: true,
				},
				Kent: worktreecontract.KentFacts{
					WorktreeID: selector, CanonicalRoot: root, DisplayName: "main", Managed: true,
				},
			},
		},
		Projection: worktreecontract.ListProjection{Selector: selector, IsCurrent: true},
	}
}

func worktreeMapperWorkspaceEntry(root string) worktreecontract.ListEntry {
	entry := worktreeMapperRegisteredEntry(root)
	entry.Projection.IsCurrent = false
	return entry
}

func worktreeMapperSetupEvents(t *testing.T) []worktreecontract.SetupEvent {
	t.Helper()
	setupOperationID, err := worktreecontract.ParseSetupOperationID("223e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	registered := worktreeMapperRegisteredEntry("/repo").Topology
	retainedPrevious := &worktreecontract.RetainedPreviousWorktree{Worktree: registered}
	scriptPath := "/repo/setup.sh"
	stdout := "stdout"
	stderr := "stderr"
	customRef := "refs/heads/custom"
	failed := func(
		cause worktreecontract.SetupFailureCause,
		readiness worktreecontract.SetupRetryReadiness,
		script *string,
		executionTarget *workflowcontract.ExecutionTargetSelection,
		retained *worktreecontract.TopologyEntry,
	) worktreecontract.SetupEvent {
		return worktreecontract.SetupEvent{
			SetupOperationID: setupOperationID,
			Phase:            worktreecontract.SetupPhaseFailed,
			Failed: &worktreecontract.SetupFailed{
				RetryReadiness:           readiness,
				Cause:                    cause,
				Diagnostic:               "setup failed",
				ScriptPath:               script,
				ExecutionTarget:          executionTarget,
				RetainedWorktree:         retained,
				RetainedPreviousWorktree: retainedPrevious,
			},
		}
	}
	return []worktreecontract.SetupEvent{
		{
			SetupOperationID: setupOperationID,
			Phase:            worktreecontract.SetupPhaseStarted,
			Started: &worktreecontract.SetupStarted{
				SourceWorkspaceRoot: "/repo", WorktreeRoot: "/repo-feature", ScriptPath: scriptPath,
			},
		},
		{
			SetupOperationID: setupOperationID,
			Phase:            worktreecontract.SetupPhaseCompleted,
			Completed:        &worktreecontract.SetupCompleted{RetainedPreviousWorktree: retainedPrevious},
		},
		{
			SetupOperationID: setupOperationID,
			Phase:            worktreecontract.SetupPhaseNotRequired,
			NotRequired: &worktreecontract.SetupNotRequired{
				Reason: worktreecontract.SetupNotRequiredNoTargetPreparation,
			},
		},
		{
			SetupOperationID: setupOperationID,
			Phase:            worktreecontract.SetupPhaseNotRequired,
			NotRequired: &worktreecontract.SetupNotRequired{
				Reason:                   worktreecontract.SetupNotRequiredNoConfiguredScript,
				RetainedPreviousWorktree: retainedPrevious,
			},
		},
		failed(
			worktreecontract.SetupFailureCause{
				Kind: worktreecontract.SetupFailureProcessExit,
				ProcessExit: &worktreecontract.SetupProcessExit{
					ExitCode: 1, Stdout: &stdout, Stderr: &stderr,
				},
			},
			worktreecontract.SetupRetryReady,
			&scriptPath,
			&workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeNone},
			&registered,
		),
		failed(
			worktreecontract.SetupFailureCause{
				Kind: worktreecontract.SetupFailureTimeout,
				Timeout: &worktreecontract.SetupTimeout{
					Stdout: &stdout, Stderr: &stderr,
				},
			},
			worktreecontract.SetupRetryReady,
			&scriptPath,
			&workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeHead},
			&registered,
		),
		failed(
			worktreecontract.SetupFailureCause{
				Kind: worktreecontract.SetupFailureTargetPreparation, Preparation: &worktreecontract.SetupPreparationFailure{},
			},
			worktreecontract.SetupRetryReady,
			nil,
			&workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeDefaultBranch},
			nil,
		),
		failed(
			worktreecontract.SetupFailureCause{
				Kind:                    worktreecontract.SetupFailureInterruptionPersistence,
				InterruptionPersistence: &worktreecontract.SetupInterruptionPersistenceFailure{},
			},
			worktreecontract.SetupNonRetryable,
			nil,
			nil,
			nil,
		),
		failed(
			worktreecontract.SetupFailureCause{
				Kind: worktreecontract.SetupFailureCanceled, Canceled: &worktreecontract.SetupCanceled{},
			},
			worktreecontract.SetupNonRetryable,
			nil,
			nil,
			nil,
		),
		failed(
			worktreecontract.SetupFailureCause{
				Kind:               worktreecontract.SetupFailureControllerShutdown,
				ControllerShutdown: &worktreecontract.SetupControllerShutdown{},
			},
			worktreecontract.SetupNonRetryable,
			nil,
			nil,
			nil,
		),
		failed(
			worktreecontract.SetupFailureCause{
				Kind: worktreecontract.SetupFailureOperational, Operational: &worktreecontract.SetupOperationalFailure{},
			},
			worktreecontract.SetupRetryReady,
			&scriptPath,
			&workflowcontract.ExecutionTargetSelection{
				Mode: workflowcontract.ExecutionTargetModeCustomRef, CustomRef: &customRef,
			},
			&registered,
		),
	}
}
