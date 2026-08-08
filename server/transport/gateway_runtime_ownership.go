package transport

import "core/shared/serverapi"

func (s *connectionState) recordOwnedRuntime(attachment serverapi.SessionRuntimeAttachment) {
	if s == nil || attachment.Validate() != nil {
		return
	}
	s.ownedRuntimesMu.Lock()
	defer s.ownedRuntimesMu.Unlock()
	if s.ownedRuntimes == nil {
		s.ownedRuntimes = make(map[serverapi.SessionRuntimeAttachment]struct{})
	}
	s.ownedRuntimes[attachment] = struct{}{}
}

func (s *connectionState) removeOwnedRuntime(attachment serverapi.SessionRuntimeAttachment) {
	if s == nil {
		return
	}
	s.ownedRuntimesMu.Lock()
	defer s.ownedRuntimesMu.Unlock()
	if len(s.ownedRuntimes) == 0 {
		return
	}
	delete(s.ownedRuntimes, attachment)
}

func (s *connectionState) takeOwnedRuntimes() []serverapi.SessionRuntimeAttachment {
	if s == nil {
		return nil
	}
	s.ownedRuntimesMu.Lock()
	defer s.ownedRuntimesMu.Unlock()
	if len(s.ownedRuntimes) == 0 {
		return nil
	}
	owned := make([]serverapi.SessionRuntimeAttachment, 0, len(s.ownedRuntimes))
	for attachment := range s.ownedRuntimes {
		owned = append(owned, attachment)
	}
	s.ownedRuntimes = nil
	return owned
}
