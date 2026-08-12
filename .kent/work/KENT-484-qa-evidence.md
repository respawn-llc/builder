# KENT-484 QA evidence

Date: 2026-08-12
Revision: `c2edce4f7` plus current review remediation

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
  -count=1` passed after moving Registry composition out of
  `runtimewirefixture`.
- `go test ./... -run '^$' -count=1` passed, confirming every Go package and
  test package type-checks without an import cycle.
- `go test ./server/core -run
  '^TestProductionGoFilesDoNotExposeTestOnlyAPIs$|^TestSmallPackagesRemainExplicitlyClassified$'
  -count=1` passed after the fixture remediation.
- `go test ./server/core -run '^TestJSONContractArchitectureGuard$' -count=1`
  passed.
- `./scripts/build.sh --output ./bin/kent` passed and produced `bin/kent`.
- `git diff --check HEAD` passed.
- Embedded `server/tools/schemas/complete_node.json` is absent.
- Invalid CLI graph input:
  `echo '{"workflow_id":' | ./bin/kent workflow graph apply --json -`
  returned exit 1 and JSON `{"outcome":"invalid_document","message":"unexpected EOF"}`.

## Remaining baseline caveat

### Full server suite reports unrelated workflow-goal failures

Focused reproduction on baseline `badbb5884`:

```text
TestServiceWorkflowRuntimeAllowsGoalControl:
session ... cannot start an ordinary execution while its workflow activation remains active
TestServiceWorkflowSessionGoalMutationAllowed:
session ... cannot start an ordinary execution while its workflow activation remains active
```

These are pre-existing baseline failures and are separate from the KENT-484
import-cycle regression.

## Prior QA environment note

The first model-loop attempt on the session-qualified instance failed during
pre-registration with duplicate `completion_mode` migration state. QA was
restarted with a wiped fresh isolated instance; the fresh model loop passed.
No production state was touched.

## Cleanup

The QA server instances used for this run were stopped and wiped. The
production server on `:53082` was not touched.

## Post-remediation status

The introduced import cycle is resolved. Registry fixtures now consume the
same runtimewire-owned static contract preparation boundary as production,
without duplicating the eight-tool source list. Concrete tool tests and the
repository-wide type-check pass. The only recorded server-suite caveat is the
unrelated Workflow Goal lifecycle failure reproduced on baseline `badbb5884`.
