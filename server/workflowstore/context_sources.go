package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type workflowRunMetadata struct {
	ContextMode            string                       `json:"context_mode"`
	ContextSource          workflow.ContextSource       `json:"context_source,omitempty"`
	SourceRunID            string                       `json:"source_run_id,omitempty"`
	SourceSessionID        string                       `json:"source_session_id,omitempty"`
	NodeOutputValues       map[string]map[string]string `json:"node_output_values,omitempty"`
	PromptTemplate         string                       `json:"prompt_template,omitempty"`
	Parameters             []workflow.Parameter         `json:"parameters,omitempty"`
	PriorParameterValues   map[string]map[string]string `json:"prior_parameter_values,omitempty"`
	TargetRunStartSnapshot *runStartSnapshot            `json:"target_run_start_snapshot,omitempty"`
}

type resolvedContextSourceRun struct {
	runID     string
	sessionID string
}

func resolvedContextSourceRunFromMetadata(ctx context.Context, q *sqlitegen.Queries, metadata workflowRunMetadata) (resolvedContextSourceRun, bool, error) {
	runID := strings.TrimSpace(metadata.SourceRunID)
	if runID == "" {
		return resolvedContextSourceRun{}, false, nil
	}
	sessionID := strings.TrimSpace(metadata.SourceSessionID)
	if sessionID == "" {
		run, err := q.GetTaskRun(ctx, runID)
		if err != nil {
			return resolvedContextSourceRun{}, true, err
		}
		sessionID = strings.TrimSpace(run.SessionID.String)
	}
	return resolvedContextSourceRun{runID: runID, sessionID: sessionID}, true, nil
}

func (s *Store) resolveContextSourceRun(ctx context.Context, q *sqlitegen.Queries, taskID string, beforeUnixMs int64, sourcePlacementID string, immediate *sqlitegen.TaskRunRecord, snapshot runStartSnapshot, edge edgeContractSnapshot) (resolvedContextSourceRun, error) {
	source := workflow.CanonicalContextSource(edge.ContextSource)
	switch source.Kind {
	case workflow.ContextSourceImmediateSource:
		if immediate == nil {
			return resolvedContextSourceRun{}, nil
		}
		return resolvedContextSourceRun{runID: immediate.ID, sessionID: strings.TrimSpace(immediate.SessionID.String)}, nil
	case workflow.ContextSourceSelectedNode:
		node, ok := snapshot.nodeByKey(source.NodeKey)
		if !ok {
			return resolvedContextSourceRun{}, fmt.Errorf("selected context source node %q missing from run snapshot", source.NodeKey)
		}
		runID, err := q.GetLatestCompletedContextSourceRun(ctx, sqlitegen.GetLatestCompletedContextSourceRunParams{TaskID: taskID, NodeID: nullableString(string(node.ID)), BeforeUnixMs: beforeUnixMs})
		if errors.Is(err, sql.ErrNoRows) {
			return resolvedContextSourceRun{}, ContextSourceNoCompletedRunError{Kind: ContextSourceKindSelected, NodeKey: string(source.NodeKey)}
		}
		if err != nil {
			return resolvedContextSourceRun{}, err
		}
		run, err := q.GetTaskRun(ctx, runID)
		if err != nil {
			return resolvedContextSourceRun{}, err
		}
		return resolvedContextSourceRun{runID: run.ID, sessionID: strings.TrimSpace(run.SessionID.String)}, nil
	case workflow.ContextSourcePreviousTarget:
		targetID := strings.TrimSpace(string(edge.TargetNode.ID))
		if targetID == "" {
			return resolvedContextSourceRun{}, errors.New("previous target context source target node missing from run snapshot")
		}
		batchID, batchScoped, err := contextSourceBatchScope(ctx, q, sourcePlacementID)
		if err != nil {
			return resolvedContextSourceRun{}, err
		}
		runID, err := latestCompletedContextSourceRun(ctx, q, taskID, targetID, beforeUnixMs, batchID, batchScoped)
		if errors.Is(err, sql.ErrNoRows) {
			targetKey := strings.TrimSpace(string(edge.TargetNode.Key))
			if targetKey == "" {
				targetKey = targetID
			}
			return resolvedContextSourceRun{}, ContextSourceNoCompletedRunError{Kind: ContextSourceKindPreviousTarget, NodeKey: targetKey}
		}
		if err != nil {
			return resolvedContextSourceRun{}, err
		}
		run, err := q.GetTaskRun(ctx, runID)
		if err != nil {
			return resolvedContextSourceRun{}, err
		}
		return resolvedContextSourceRun{runID: run.ID, sessionID: strings.TrimSpace(run.SessionID.String)}, nil
	default:
		return resolvedContextSourceRun{}, fmt.Errorf("context source kind %q is invalid", source.Kind)
	}
}

func contextSourceBatchScope(ctx context.Context, q *sqlitegen.Queries, sourcePlacementID string) (string, bool, error) {
	placementID := strings.TrimSpace(sourcePlacementID)
	if placementID == "" {
		return "", false, nil
	}
	batchID, err := q.GetContextSourceBatchScope(ctx, placementID)
	if err != nil {
		return "", false, err
	}
	trimmed := strings.TrimSpace(batchID.String)
	return trimmed, batchID.Valid && trimmed != "", nil
}

func latestCompletedContextSourceRun(ctx context.Context, q *sqlitegen.Queries, taskID string, nodeID string, beforeUnixMs int64, batchID string, batchScoped bool) (string, error) {
	if !batchScoped {
		return q.GetLatestCompletedContextSourceRun(ctx, sqlitegen.GetLatestCompletedContextSourceRunParams{TaskID: taskID, NodeID: nullableString(nodeID), BeforeUnixMs: beforeUnixMs})
	}
	return q.GetLatestCompletedContextSourceRunInBatch(ctx, sqlitegen.GetLatestCompletedContextSourceRunInBatchParams{TaskID: taskID, NodeID: nullableString(nodeID), BatchID: sql.NullString{String: batchID, Valid: true}, BeforeUnixMs: beforeUnixMs})
}

func (s *Store) resolvePromptPriorParameterValues(ctx context.Context, q *sqlitegen.Queries, taskID string, beforeUnixMs int64, sourcePlacementID string, edge edgeContractSnapshot) (map[string]map[string]string, error) {
	refs, err := workflow.ExtractPromptTemplateReferences(edge.PromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse transition prompt references: %w", err)
	}
	if len(refs.Invalid) > 0 {
		return nil, fmt.Errorf("transition prompt references are invalid: %s", refs.Invalid[0].Message)
	}
	if len(refs.PriorParams) == 0 {
		return nil, nil
	}
	batchID, batchScoped, err := contextSourceBatchScope(ctx, q, sourcePlacementID)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	seen := map[string]map[string]bool{}
	for _, ref := range refs.PriorParams {
		transitionKey := strings.TrimSpace(string(ref.TransitionKey))
		parameterKey := strings.TrimSpace(ref.ParameterKey)
		if transitionKey == "" || parameterKey == "" {
			return nil, fmt.Errorf("transition prompt prior parameter reference %q is invalid", ref.Placeholder)
		}
		if seen[transitionKey] != nil && seen[transitionKey][parameterKey] {
			continue
		}
		if seen[transitionKey] == nil {
			seen[transitionKey] = map[string]bool{}
		}
		seen[transitionKey][parameterKey] = true
		value, err := latestTransitionParameterValue(ctx, q, taskID, transitionKey, parameterKey, beforeUnixMs, batchID, batchScoped)
		if err != nil {
			return nil, err
		}
		if out[transitionKey] == nil {
			out[transitionKey] = map[string]string{}
		}
		out[transitionKey][parameterKey] = value
	}
	return out, nil
}

func latestTransitionParameterValue(ctx context.Context, q *sqlitegen.Queries, taskID string, transitionKey string, parameterKey string, beforeUnixMs int64, batchID string, batchScoped bool) (string, error) {
	outputValuesJSON, err := latestTransitionOutputValuesJSON(ctx, q, taskID, transitionKey, beforeUnixMs, batchID, batchScoped)
	if err != nil {
		return "", err
	}
	outputValues := map[string]string{}
	if err := workflow.UnmarshalString(outputValuesJSON, &outputValues); err != nil {
		return "", err
	}
	value := outputValues[parameterKey]
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("prior transition %q missing parameter %q", transitionKey, parameterKey)
	}
	return value, nil
}

func latestTransitionOutputValuesJSON(ctx context.Context, q *sqlitegen.Queries, taskID string, transitionKey string, beforeUnixMs int64, batchID string, batchScoped bool) (string, error) {
	if batchScoped {
		scopedOutputValuesJSON, err := q.GetLatestTransitionOutputValuesInBatch(ctx, sqlitegen.GetLatestTransitionOutputValuesInBatchParams{TaskID: taskID, TransitionID: transitionKey, BatchID: sql.NullString{String: batchID, Valid: true}, BeforeUnixMs: beforeUnixMs})
		if err == nil {
			return scopedOutputValuesJSON, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	outputValuesJSON, err := q.GetLatestTransitionOutputValues(ctx, sqlitegen.GetLatestTransitionOutputValuesParams{TaskID: taskID, TransitionID: transitionKey, BeforeUnixMs: beforeUnixMs})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("prior transition %q has no completed output for task", transitionKey)
	}
	if err != nil {
		return "", err
	}
	return outputValuesJSON, nil
}

func clonePriorParameterValues(values map[string]map[string]string) map[string]map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(values))
	for transitionKey, params := range values {
		if len(params) == 0 {
			continue
		}
		out[transitionKey] = make(map[string]string, len(params))
		for parameterKey, value := range params {
			out[transitionKey][parameterKey] = value
		}
	}
	return out
}
