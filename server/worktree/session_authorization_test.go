package worktree

import (
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestReadWorktreeRoutesValidatedUseAuthorizedExecutionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	sessionID, err := runtimeids.ParseSessionID(env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	authorizedTarget := clientui.SessionExecutionTarget{
		WorkspaceID:      env.binding.WorkspaceID,
		WorkspaceRoot:    env.workspaceRoot,
		CwdRelpath:       ".",
		EffectiveWorkdir: env.workspaceRoot,
	}
	authorization := apicontract.AuthorizedSessionInActiveProject{
		SessionID:       sessionID,
		ActiveProjectID: env.binding.ProjectID,
		OwningProjectID: env.binding.ProjectID,
		ExecutionTarget: authorizedTarget,
	}
	created := mustCreateWorktree(t, env, "feature/authorized-delete-preview")

	tests := []struct {
		name string
		run  func(context.Context) (clientui.SessionExecutionTarget, error)
	}{
		{
			name: "status",
			run: func(ctx context.Context) (clientui.SessionExecutionTarget, error) {
				return withValidatedWorktreeRequest(
					serverapi.WorktreeStatusRequest{SessionID: sessionID.String()},
					func(validated apicontract.Validated[serverapi.WorktreeStatusRequest]) (clientui.SessionExecutionTarget, error) {
						response, err := env.service.GetWorktreeStatusValidated(ctx, validated, authorization)
						return response.Target, err
					},
				)
			},
		},
		{
			name: "list",
			run: func(ctx context.Context) (clientui.SessionExecutionTarget, error) {
				return withValidatedWorktreeRequest(
					serverapi.WorktreeListRequest{SessionID: sessionID.String()},
					func(validated apicontract.Validated[serverapi.WorktreeListRequest]) (clientui.SessionExecutionTarget, error) {
						response, err := env.service.ListWorktreesValidated(ctx, validated, authorization)
						return response.Target, err
					},
				)
			},
		},
		{
			name: "selector preview",
			run: func(ctx context.Context) (clientui.SessionExecutionTarget, error) {
				_, err := withValidatedWorktreeRequest(
					serverapi.WorktreeSelectorPreviewRequest{SessionID: sessionID.String(), Selector: env.workspaceRoot},
					func(validated apicontract.Validated[serverapi.WorktreeSelectorPreviewRequest]) (struct{}, error) {
						_, err := env.service.ResolveWorktreeSelectorValidated(ctx, validated, authorization)
						return struct{}{}, err
					},
				)
				return authorizedTarget, err
			},
		},
		{
			name: "delete preview",
			run: func(ctx context.Context) (clientui.SessionExecutionTarget, error) {
				_, err := withValidatedWorktreeRequest(
					serverapi.WorktreeDeletePreviewRequest{SessionID: sessionID.String(), Selector: created.WorktreeID},
					func(validated apicontract.Validated[serverapi.WorktreeDeletePreviewRequest]) (struct{}, error) {
						_, err := env.service.PreviewWorktreeDeleteValidated(ctx, validated, authorization)
						return struct{}{}, err
					},
				)
				return authorizedTarget, err
			},
		},
		{
			name: "create target resolve",
			run: func(ctx context.Context) (clientui.SessionExecutionTarget, error) {
				_, err := withValidatedWorktreeRequest(
					serverapi.WorktreeCreateTargetResolveRequest{SessionID: sessionID.String(), Target: "HEAD"},
					func(validated apicontract.Validated[serverapi.WorktreeCreateTargetResolveRequest]) (struct{}, error) {
						_, err := env.service.ResolveWorktreeCreateTargetValidated(ctx, validated, authorization)
						return struct{}{}, err
					},
				)
				return authorizedTarget, err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, err := test.run(t.Context())
			if err != nil {
				t.Fatalf("validated Worktree route: %v", err)
			}
			if target != authorizedTarget {
				t.Fatalf("target = %+v, want authorized target %+v", target, authorizedTarget)
			}
		})
	}
}

func withValidatedWorktreeRequest[Req any, Resp any](
	request Req,
	use func(apicontract.Validated[Req]) (Resp, error),
) (Resp, error) {
	return apicontract.WithValidated(request, apicontract.SemanticValidationRequired, use)
}
