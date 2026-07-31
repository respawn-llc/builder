package runtimecommand

import (
	"context"
	"errors"
)

type BoundaryKind uint8

const (
	AgentStepBoundary BoundaryKind = iota + 1
	AgentTurnBoundary
)

type BoundarySelectionKind uint8

const (
	BoundarySelectionEligiblePrefix BoundarySelectionKind = iota + 1
	BoundarySelectionExplicitIDs
	BoundarySelectionAllPending
)

type BoundarySelection struct {
	Kind BoundarySelectionKind
	IDs  []string
}

type BoundaryClaim struct {
	Target    Target
	Boundary  BoundaryKind
	Selection BoundarySelection
}

func (c BoundaryClaim) Validate() error {
	if err := c.Target.Validate(); err != nil {
		return err
	}
	if c.Boundary != AgentStepBoundary && c.Boundary != AgentTurnBoundary {
		return errors.New("runtime command boundary kind is required")
	}
	switch c.Selection.Kind {
	case BoundarySelectionEligiblePrefix, BoundarySelectionAllPending:
		if len(c.Selection.IDs) != 0 {
			return errors.New("runtime command boundary selection cannot include ids")
		}
	case BoundarySelectionExplicitIDs:
		if len(c.Selection.IDs) == 0 {
			return errors.New("runtime command boundary explicit ids are required")
		}
	default:
		return errors.New("runtime command boundary selection kind is required")
	}
	return nil
}

func EnqueueBoundaryClaim[T any](
	ctx context.Context,
	authority *Authority,
	claim BoundaryClaim,
	apply func(Turn, BoundaryClaim) (T, error),
) (Future[T], error) {
	if err := claim.Validate(); err != nil {
		return Future[T]{}, err
	}
	if apply == nil {
		return Future[T]{}, ErrCommandHandlerNeeded
	}
	return Enqueue(ctx, authority, claim.Target, func(turn Turn) (T, error) {
		return apply(turn, claim)
	})
}
