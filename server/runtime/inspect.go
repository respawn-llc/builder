package runtime

import (
	"context"
	"errors"

	"core/server/llm"
	"github.com/google/uuid"
)

// ErrInspectionExactTokenCountRequired reports that an offline inspector cannot
// safely reproduce a live compaction decision without provider token counting.
var ErrInspectionExactTokenCountRequired = errors.New("offline inspection requires provider input-token counting near the compaction threshold")

// PrepareInspectionRequest builds the provider-agnostic llm.Request for a session
// using the exact production request-assembly path, WITHOUT running a model turn
// or performing any network I/O. It is an operator-only diagnostic seam used by
// offline inspection tooling to capture the request shape that would be sent to a
// provider.
//
// The engine must already be constructed (which auto-hydrates the active transcript
// segment via restoreMessages). It prepares the same meta context that a live turn
// prepares before running the same pre-dispatch preparation as a live turn. If
// that preparation requires a model-backed compaction, inspection returns the
// preparation error rather than emitting a stale post-preparation payload.
// allowTools mirrors the production tool-exposure behavior; pass false to
// produce a tool-less payload.
func PrepareInspectionRequest(ctx context.Context, eng *Engine, allowTools bool) (llm.Request, error) {
	eng.ensureOrchestrationCollaborators()
	stepID := uuid.NewString()
	if err := eng.ensureMetaContextForRequest(ctx, stepID); err != nil {
		return llm.Request{}, err
	}
	if eng.inspectionRequiresExactTokenCount(ctx) {
		return llm.Request{}, ErrInspectionExactTokenCountRequired
	}
	if err := (&defaultStepExecutor{engine: eng}).prepareModelTurn(ctx, stepID); err != nil {
		return llm.Request{}, err
	}
	return eng.buildRequest(ctx, stepID, allowTools)
}

func (e *Engine) inspectionRequiresExactTokenCount(ctx context.Context) bool {
	snapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if !planner.autoCompactionAvailable(snapshot) {
		return false
	}
	limit := planner.autoCompactTokenLimit(snapshot)
	if limit <= 0 {
		return false
	}
	caps, err := e.providerCapabilities(ctx)
	if err != nil || !caps.SupportsRequestInputTokenCount {
		return false
	}
	total := e.currentTokenUsage() + planner.reservedOutputTokens(snapshot)
	return total+autoCompactPrecisionMarginForLimit(limit) >= limit
}
