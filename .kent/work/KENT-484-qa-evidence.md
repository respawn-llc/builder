# KENT-484 QA evidence

Date: 2026-08-12
Revision: `6339eb7de`

## Passed checks

- `.kent/qa/qa-harness.sh doctor`: ready; isolated QA prerequisites and local
  OMLX were available.
- Fresh isolated headless model loop:
  `env -u KENT_SESSION_ID KENT_QA_INSTANCE=kent484-manual ./.kent/qa/qa-harness.sh run "Respond with exactly: QA model loop ready"`
  returned `QA model loop ready`.
- `go test ./server/tools ./server/runtimewire ./server/workflowruntime
  ./server/llm ./cli/kent ./shared/jsoncontract ./server/metadata ./server/core
  -run 'TestJSONContractArchitectureGuard|TestStaticToolContracts|TestRegistryPrepareInput|TestCompletion|TestWorkflowGraphDocument|TestTaskComplete|TestMigrationInputBindings|TestOpenAI' -count=1`
  passed.
- `go test ./server/tools/patch ./server/tools/edit ./server/tools/readimage
  -count=1` passed on the baseline revision, confirming the current import-cycle
  failure is introduced by KENT-484.
- `go test ./server/core -run '^TestJSONContractArchitectureGuard$' -count=1`
  passed.
- `./scripts/build.sh --output ./bin/kent` passed and produced `bin/kent`.
- `git diff --check HEAD` passed.
- Embedded `server/tools/schemas/complete_node.json` is absent.
- Invalid CLI graph input:
  `echo '{"workflow_id":' | ./bin/kent workflow graph apply --json -`
  returned exit 1 and JSON `{"outcome":"invalid_document","message":"unexpected EOF"}`.

## Failures

### 1. Server suite fails with introduced test import cycles — implementation owner

`./scripts/test.sh server` failed. The failure is reproducible directly on the
current revision:

```text
# core/server/tools/patch
package core/server/tools/patch
    imports core/internal/testharness/runtimewirefixture from tool_part2_test.go
    imports core/server/tools/patch from registry.go: import cycle not allowed in test
# core/server/tools/edit
... imports core/server/tools/edit from registry.go: import cycle not allowed in test
# core/server/tools/readimage
... imports core/server/tools/readimage from registry.go: import cycle not allowed in test
```

The same package commands pass on baseline `badbb5884` (no tests to run), so
this is a KENT-484 regression, not an environmental failure. The helper
`internal/testharness/runtimewirefixture/registry.go` imports concrete tool
packages while being used by their internal package tests.

The same cycle prevents `TestProductionGoFilesDoNotExposeTestOnlyAPIs` from
type-checking in `server/core`.

### 2. Full server suite also reports baseline workflow-goal failures

Focused reproduction on baseline `badbb5884`:

```text
TestServiceWorkflowRuntimeAllowsGoalControl:
session ... cannot start an ordinary execution while its workflow activation remains active
TestServiceWorkflowSessionGoalMutationAllowed:
session ... cannot start an ordinary execution while its workflow activation remains active
```

These are pre-existing baseline failures and are separate from the KENT-484
import-cycle regression.

### 3. Stale QA instance migration failure

The first model-loop attempt on the session-qualified instance failed during
pre-registration with duplicate `completion_mode` migration state. QA was
restarted with a wiped fresh isolated instance; the fresh model loop passed.
No production state was touched.

## Cleanup

The QA server instances used for this run were stopped and wiped. The
production server on `:53082` was not touched.

## Verdict

**QA failed** because the KENT-484 revision introduces test import cycles that
break the required server suite and architecture type-checking guard.
