package workflowview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
)

type sessionNameLookup func(context.Context, []string) ([]sqlitegen.ListSessionNamesByIDsRow, error)

func resolveSessionNames(
	ctx context.Context,
	lookup sessionNameLookup,
	sessionIDs []string,
) (map[string]*string, error) {
	if lookup == nil {
		return nil, errors.New("session name lookup is required")
	}
	uniqueIDs := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if strings.TrimSpace(sessionID) == "" {
			return nil, errors.New("session name lookup requested a blank session id")
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		uniqueIDs = append(uniqueIDs, sessionID)
	}
	if len(uniqueIDs) == 0 {
		return map[string]*string{}, nil
	}
	rows, err := lookup(ctx, uniqueIDs)
	if err != nil {
		return nil, err
	}
	names := make(map[string]*string, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" {
			return nil, errors.New("session name lookup returned a blank session id")
		}
		if _, exists := names[row.ID]; exists {
			return nil, fmt.Errorf("session name lookup returned duplicate session %q", row.ID)
		}
		var name *string
		switch {
		case row.Name == "":
			// TODO(KENT-220): Delete the exact-empty compatibility branch after Session names migrate to null.
		case strings.TrimSpace(row.Name) == "":
			return nil, fmt.Errorf("session name lookup returned a blank name for session %q", row.ID)
		default:
			value := row.Name
			name = &value
		}
		names[row.ID] = name
	}
	for _, sessionID := range uniqueIDs {
		if _, exists := names[sessionID]; !exists {
			return nil, fmt.Errorf("session %q has no persisted metadata", sessionID)
		}
	}
	return names, nil
}
