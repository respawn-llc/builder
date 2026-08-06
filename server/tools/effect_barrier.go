package tools

import (
	"context"
	"errors"
)

type EffectBarrierReason uint8

const (
	EffectBarrierQuestion EffectBarrierReason = iota + 1
	EffectBarrierApproval
	EffectBarrierCompleteNode
)

type EffectBarrier func(EffectBarrierReason) error

type effectBarrierContextKey struct{}

func WithEffectBarrier(ctx context.Context, barrier EffectBarrier) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if barrier == nil {
		panic("tool effect barrier is required")
	}
	return context.WithValue(ctx, effectBarrierContextKey{}, barrier)
}

func EffectBarrierFromContext(ctx context.Context) (EffectBarrier, bool) {
	if ctx == nil {
		return nil, false
	}
	barrier, ok := ctx.Value(effectBarrierContextKey{}).(EffectBarrier)
	return barrier, ok
}

func effectBarrierReasonForAsk(req AskQuestionRequest) (EffectBarrierReason, error) {
	if req.Approval {
		return EffectBarrierApproval, nil
	}
	if req.Question == "" {
		return 0, errors.New("question is required")
	}
	return EffectBarrierQuestion, nil
}
