package workflowview

import (
	"context"
	"strings"

	"core/server/metadata/sqlitegen"
)

func loadSessionNamesByRun(ctx context.Context, queries *sqlitegen.Queries, runs []sqlitegen.TaskRunRecord) (map[string]string, error) {
	sessionIDs := []string{}
	seen := map[string]bool{}
	for _, run := range runs {
		sessionID := strings.TrimSpace(run.SessionID.String)
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true
		sessionIDs = append(sessionIDs, sessionID)
	}
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := queries.ListSessionNamesByIDs(ctx, sessionIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = strings.TrimSpace(row.Name)
	}
	return out, nil
}

func loadTransitionEdgesByTransitionID(ctx context.Context, queries *sqlitegen.Queries, transitions []sqlitegen.TaskTransitionRecord) (map[string][]sqlitegen.TaskTransitionEdgeRecord, error) {
	if len(transitions) == 0 {
		return map[string][]sqlitegen.TaskTransitionEdgeRecord{}, nil
	}
	ids := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		ids = append(ids, transition.ID)
	}
	rows, err := queries.ListTaskTransitionEdgesByTransitionIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]sqlitegen.TaskTransitionEdgeRecord, len(transitions))
	for _, row := range rows {
		out[row.TaskTransitionID] = append(out[row.TaskTransitionID], row)
	}
	return out, nil
}
