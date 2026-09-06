package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/shared/serverapi"
)

func (s *Store) ChatSettingsTaskIdentityForSession(
	ctx context.Context,
	sessionID string,
) (*serverapi.ChatSettingsTaskIdentity, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, errors.New("session id is required")
	}
	row, err := s.queries.GetSessionChatSettingsTaskIdentity(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Session Chat Settings Task identity: %w", err)
	}
	if strings.TrimSpace(row.TaskID) == "" || strings.TrimSpace(row.TaskShortID) == "" {
		return nil, errors.New("Session Chat Settings Task identity is invalid")
	}
	identity := serverapi.ChatSettingsTaskIdentity{
		TaskID:      row.TaskID,
		TaskShortID: row.TaskShortID,
	}
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("validate Session Chat Settings Task identity: %w", err)
	}
	return &identity, nil
}
