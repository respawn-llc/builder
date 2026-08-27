import { create, operationName } from "@app/server-api-contract";
import { AuthRequiredDetailsSchema } from "@app/server-api-contract/gen/kent/api/auth/auth_pb";
import { WorkspaceNotRegisteredDetailsSchema } from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  ServerNotReadyDetailsSchema,
  ServerNotReadyReason,
} from "@app/server-api-contract/gen/kent/api/server/server_pb";
import { InternalFailureDetailsSchema } from "@app/server-api-contract/gen/kent/api/shared/foundation_pb";
import {
  BlockedDetailsSchema,
  CreateErrorSchema,
  CreateErrorOwner,
  CreateFailureDetailsSchema,
  CreateService,
  DeleteErrorSchema,
  DeletePreconditionDetailsSchema,
  DirtyStateKind,
  DirtyStateSchema,
  ImmediateTransitionDetailsSchema,
  ImmediateTransitionErrorKind,
  ListService,
  SelectorErrorDetailsSchema,
  SelectorErrorKind,
  SelectorResolveErrorSchema,
  SelectorService,
  SetupRetainedDetailsSchema,
  StatusErrorSchema,
  StatusService,
  TransitionPendingDetailsSchema,
  TransitionService,
  WorkspaceListErrorSchema,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ContractError, RpcError, errorMessage } from "./errors";
import { WorktreeError, WorktreeSetupRetainedError, projectWorktreeFailure } from "./worktreeFailure";

describe("generated Worktree failure projection", () => {
  it.each([
    {
      name: "selector",
      method: SelectorService.method.resolve,
      failure: create(SelectorResolveErrorSchema, {
        code: "selector_error",
        detail: {
          case: "selectorError",
          value: create(SelectorErrorDetailsSchema, {
            kind: SelectorErrorKind.WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND,
            input: "feature",
          }),
        },
      }),
      kind: "selector",
    },
    {
      name: "create diagnostic",
      method: CreateService.method.create,
      failure: create(CreateErrorSchema, {
        code: "create_failed",
        detail: {
          case: "createFailed",
          value: create(CreateFailureDetailsSchema, {
            owner: CreateErrorOwner.WORKTREE_CREATE_ERROR_OWNER_FORM,
            diagnostic: "dynamic create diagnostic",
          }),
        },
      }),
      kind: "create",
      diagnostic: "dynamic create diagnostic",
    },
    {
      name: "transition pending",
      method: TransitionService.method.delete,
      failure: create(DeleteErrorSchema, {
        code: "transition_pending",
        detail: {
          case: "transitionPending",
          value: create(TransitionPendingDetailsSchema, {
            sessionId: "session-1",
            pendingOperationId: "123e4567-e89b-42d3-a456-426614174000",
          }),
        },
      }),
      kind: "transition_pending",
    },
    {
      name: "immediate diagnostic",
      method: TransitionService.method.delete,
      failure: create(DeleteErrorSchema, {
        code: "immediate_transition",
        detail: {
          case: "immediateTransition",
          value: create(ImmediateTransitionDetailsSchema, {
            kind: ImmediateTransitionErrorKind.WORKTREE_IMMEDIATE_TRANSITION_APPLY_FAILED,
            diagnostic: "dynamic immediate diagnostic",
          }),
        },
      }),
      kind: "immediate_transition",
      diagnostic: "dynamic immediate diagnostic",
    },
    {
      name: "dirty delete precondition",
      method: TransitionService.method.delete,
      failure: create(DeleteErrorSchema, {
        code: "delete_precondition",
        detail: {
          case: "deletePrecondition",
          value: create(DeletePreconditionDetailsSchema, {
            dirtyState: create(DirtyStateSchema, {
              kind: DirtyStateKind.DIRTY_STATE_DIRTY,
              dirtyFileCount: 2,
            }),
          }),
        },
      }),
      kind: "delete_precondition",
    },
    {
      name: "unknown delete precondition",
      method: TransitionService.method.delete,
      failure: create(DeleteErrorSchema, {
        code: "delete_precondition",
        detail: {
          case: "deletePrecondition",
          value: create(DeletePreconditionDetailsSchema, {
            dirtyState: create(DirtyStateSchema, {
              kind: DirtyStateKind.DIRTY_STATE_UNKNOWN,
              unknownCause: "dynamic inspection diagnostic",
            }),
          }),
        },
      }),
      kind: "delete_precondition",
      diagnostic: "dynamic inspection diagnostic",
    },
    {
      name: "blocked diagnostic",
      method: TransitionService.method.delete,
      failure: create(DeleteErrorSchema, {
        code: "worktree_blocked",
        detail: {
          case: "worktreeBlocked",
          value: create(BlockedDetailsSchema, { diagnostic: "dynamic blocked diagnostic" }),
        },
      }),
      kind: "blocked",
      diagnostic: "dynamic blocked diagnostic",
    },
  ])("$name", ({ method, failure, kind, diagnostic }) => {
    const projected = projectWorktreeFailure(method, failure);
    expect(projected).toBeInstanceOf(WorktreeError);
    if (!(projected instanceof WorktreeError)) throw new Error("expected WorktreeError");
    expect(projected.detail.kind).toBe(kind);
    expect(errorMessage(projected)).not.toContain(
      `${operationName(method)} failed with code ${failure.code}`,
    );
    if (diagnostic !== undefined) expect(errorMessage(projected)).toContain(diagnostic);
  });

  it("projects retained setup facts and their diagnostic", () => {
    const projected = projectWorktreeFailure(
      CreateService.method.create,
      create(CreateErrorSchema, {
        code: "setup_retained",
        detail: {
          case: "setupRetained",
          value: create(SetupRetainedDetailsSchema, {
            worktree: {
              git: {
                canonicalRoot: "/repo/feature",
                headObject: "abc123",
                branchRef: "refs/heads/feature",
                branchName: "feature",
                detached: false,
                bare: false,
                isMain: false,
                pathAvailable: true,
              },
              kent: {
                worktreeId: "worktree-1",
                canonicalRoot: "/repo/feature",
                displayName: "feature",
                managed: true,
                createdBranch: true,
              },
            },
            scriptPath: "/repo/setup.sh",
            diagnostic: "dynamic setup diagnostic",
          }),
        },
      }),
    );

    expect(projected).toBeInstanceOf(WorktreeSetupRetainedError);
    expect(projected).toMatchObject({
      diagnostic: "dynamic setup diagnostic",
      scriptPath: "/repo/setup.sh",
      worktree: { variant: "registered" },
    });
    expect(errorMessage(projected)).toContain("dynamic setup diagnostic");
  });

  it.each([
    {
      name: "auth required",
      method: StatusService.method.get,
      failure: create(StatusErrorSchema, {
        code: "auth_required",
        detail: {
          case: "authRequired",
          value: create(AuthRequiredDetailsSchema),
        },
      }),
    },
    {
      name: "workspace registration",
      method: ListService.method.listWorkspace,
      failure: create(WorkspaceListErrorSchema, {
        code: "workspace_not_registered",
        detail: {
          case: "workspaceNotRegistered",
          value: create(WorkspaceNotRegisteredDetailsSchema, {
            projectId: "project-1",
            workspaceId: "workspace-1",
          }),
        },
      }),
    },
    {
      name: "server readiness",
      method: StatusService.method.get,
      failure: create(StatusErrorSchema, {
        code: "server_not_ready",
        detail: {
          case: "serverNotReady",
          value: create(ServerNotReadyDetailsSchema, {
            reason: ServerNotReadyReason.ACTIVATION_FAILED,
            onboardingCompleted: true,
            diagnostic: "dynamic readiness diagnostic",
          }),
        },
      }),
    },
    {
      name: "internal failure",
      method: StatusService.method.get,
      failure: create(StatusErrorSchema, {
        code: "internal_failure",
        detail: {
          case: "internalFailure",
          value: create(InternalFailureDetailsSchema, {
            operation: "worktree.status",
            cause: "dynamic internal diagnostic",
          }),
        },
      }),
    },
  ])("$name preserves generated data and projected presentation", ({ method, failure }) => {
    const projected = projectWorktreeFailure(method, failure);
    expect(projected).toBeInstanceOf(RpcError);
    expect(projected.data).toBe(failure);
    expect(errorMessage(projected)).not.toContain(
      `${operationName(method)} failed with code ${failure.code}`,
    );
  });

  it("keeps unknown future failures generic and rejects malformed known failures", () => {
    const method = StatusService.method.get;
    const future = create(StatusErrorSchema, {
      code: "future_failure",
      detail: { case: undefined },
    });
    const projected = projectWorktreeFailure(method, future);
    expect(projected).toBeInstanceOf(RpcError);
    expect(projected).not.toBeInstanceOf(WorktreeError);
    expect(projected).toMatchObject({
      method: operationName(method),
      data: future,
    });

    expect(() =>
      projectWorktreeFailure(
        method,
        create(StatusErrorSchema, {
          code: "server_not_ready",
          detail: { case: undefined },
        }),
      ),
    ).toThrow(ContractError);
    expect(() =>
      projectWorktreeFailure(
        method,
        create(StatusErrorSchema, {
          code: "server_not_ready",
          detail: {
            case: "authRequired",
            value: create(AuthRequiredDetailsSchema),
          },
        }),
      ),
    ).toThrow(ContractError);
    expect(() =>
      projectWorktreeFailure(
        method,
        create(StatusErrorSchema, {
          code: "",
          detail: {
            case: "authRequired",
            value: create(AuthRequiredDetailsSchema),
          },
        }),
      ),
    ).toThrow(ContractError);
  });
});
