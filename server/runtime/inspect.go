package runtime

import (
	"context"

	"core/server/llm"
	"github.com/google/uuid"
)

// PrepareInspectionRequest builds the provider-agnostic llm.Request for a session
// using the exact production request-assembly path, WITHOUT running a model turn
// or performing any network I/O. It is an operator-only diagnostic seam used by
// offline inspection tooling to capture the request shape that would be sent to a
// provider.
//
// The engine must already be constructed (which auto-hydrates the active transcript
// segment via restoreMessages). It prepares the same meta context that a live turn
// prepares before building the request. allowTools mirrors the production
// tool-exposure behavior; pass false to produce a tool-less payload.
func PrepareInspectionRequest(ctx context.Context, eng *Engine, allowTools bool) (llm.Request, error) {
	stepID := uuid.NewString()
	if err := eng.ensureMetaContextForRequest(ctx, stepID); err != nil {
		return llm.Request{}, err
	}
	return eng.buildRequest(ctx, stepID, allowTools)
}
