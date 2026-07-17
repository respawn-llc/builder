package launch

import (
	"context"
	"errors"
	"fmt"

	"core/server/session"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/runtimeids"
)

type parentAgentDepthPolicy struct {
	sessions session.PersistedSessionResolver
}

func (p parentAgentDepthPolicy) enforce(ctx context.Context, immediate session.Meta, maxDepth int, debug bool) error {
	if p.sessions == nil {
		return errors.New("persisted session resolver is required")
	}
	if maxDepth < 0 || maxDepth > config.MaxSupportedSubagentDepth {
		return fmt.Errorf("max subagent depth %d is outside supported range 0..%d", maxDepth, config.MaxSupportedSubagentDepth)
	}
	immediateID, err := runtimeids.ParseSessionID(immediate.SessionID)
	if err != nil {
		return fmt.Errorf("invalid immediate parent-agent session id: %w", err)
	}
	visited := []runtimeids.SessionID{immediateID}
	seen := map[runtimeids.SessionID]struct{}{immediateID: {}}
	proposedDepth := 1
	if proposedDepth > maxDepth {
		return protocol.NewMaxDepthExceededSubagentLaunchPolicyError(proposedDepth, maxDepth)
	}
	current := immediate
	for current.ParentAgentSessionID != nil {
		ancestorID := *current.ParentAgentSessionID
		if _, repeated := seen[ancestorID]; repeated {
			policyErr := protocol.NewLineageCorruptSubagentLaunchPolicyError(ancestorID, visited)
			if debug {
				panic(policyErr)
			}
			return policyErr
		}
		proposedDepth++
		if proposedDepth > maxDepth {
			return protocol.NewMaxDepthExceededSubagentLaunchPolicyError(proposedDepth, maxDepth)
		}
		seen[ancestorID] = struct{}{}
		visited = append(visited, ancestorID)
		record, err := p.sessions.ResolvePersistedSession(ctx, ancestorID.String())
		if errors.Is(err, session.ErrSessionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if record.Meta == nil {
			return errors.New("persisted parent-agent session metadata is required")
		}
		resolvedID, err := runtimeids.ParseSessionID(record.Meta.SessionID)
		if err != nil {
			return fmt.Errorf("invalid persisted parent-agent session id: %w", err)
		}
		if resolvedID != ancestorID {
			return fmt.Errorf("persisted parent-agent session id mismatch: requested %q, resolved %q", ancestorID, resolvedID)
		}
		current = *record.Meta
	}
	return nil
}
