package transport

import "strings"

func (s *connectionState) recordOwnedRuntime(sessionID string) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	if s.ownedRuntimes == nil {
		s.ownedRuntimes = make(map[string]struct{})
	}
	s.ownedRuntimes[trimmedSessionID] = struct{}{}
}

func (s *connectionState) removeOwnedRuntime(sessionID string) {
	if s == nil || len(s.ownedRuntimes) == 0 {
		return
	}
	delete(s.ownedRuntimes, strings.TrimSpace(sessionID))
}

func (s *connectionState) takeOwnedRuntimes() []string {
	if s == nil || len(s.ownedRuntimes) == 0 {
		return nil
	}
	owned := make([]string, 0, len(s.ownedRuntimes))
	for sessionID := range s.ownedRuntimes {
		owned = append(owned, sessionID)
	}
	s.ownedRuntimes = nil
	return owned
}
