package runtime

import (
	"context"

	"core/server/session"
)

func (e *Engine) appendCommittedEntryWithCommitReceipt(ctx context.Context, entry storedLocalEntry) (session.CommitReceipt, error) {
	return awaitEngineRuntimeOperation(
		ctx,
		e,
		func(context.Context) (session.CommitReceipt, error) {
			return e.appendCommittedEntryWithCommitReceiptRaw(entry)
		},
	)
}

func (e *Engine) appendCommittedEntryWithCommitReceiptRaw(entry storedLocalEntry) (session.CommitReceipt, error) {
	if entry.Role == "" || entry.Text == "" {
		return session.CommitReceipt{}, nil
	}
	return e.steerWithCommitReceiptRaw(sessionSteeringProvenance(), steerLocalEntryIntent(entry))
}
