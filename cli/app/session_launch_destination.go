package app

import (
	"errors"
	"strings"
)

type validatedSessionID struct {
	value string
}

func newValidatedSessionID(raw string) (validatedSessionID, error) {
	validated := optionalValidatedSessionID(raw)
	if validated == nil {
		return validatedSessionID{}, errors.New("session id is required")
	}
	return *validated, nil
}

func optionalValidatedSessionID(raw string) *validatedSessionID {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return nil
	}
	return &validatedSessionID{value: normalized}
}

func (id validatedSessionID) String() string {
	return id.value
}

type sessionLaunchDestination interface {
	sessionLaunchDestination()
}

type sessionPickerDestination struct{}

func (sessionPickerDestination) sessionLaunchDestination() {}

type sessionOpenDestination struct {
	sessionID validatedSessionID
}

func newSessionOpenDestination(sessionID string) (sessionOpenDestination, error) {
	validated, err := newValidatedSessionID(sessionID)
	if err != nil {
		return sessionOpenDestination{}, err
	}
	return sessionOpenDestination{sessionID: validated}, nil
}

func (sessionOpenDestination) sessionLaunchDestination() {}

func (d sessionOpenDestination) SessionID() string {
	return d.sessionID.String()
}

type sessionParentReference struct {
	sessionID validatedSessionID
}

func newSessionParentReference(sessionID string) (sessionParentReference, error) {
	validated, err := newValidatedSessionID(sessionID)
	if err != nil {
		return sessionParentReference{}, err
	}
	return sessionParentReference{sessionID: validated}, nil
}

func (p sessionParentReference) SessionID() string {
	return p.sessionID.String()
}

func optionalSessionParentReference(sessionID string) *sessionParentReference {
	validated := optionalValidatedSessionID(sessionID)
	if validated == nil {
		return nil
	}
	return &sessionParentReference{sessionID: *validated}
}

type sessionCreateDestination struct {
	Parent *sessionParentReference
}

func (sessionCreateDestination) sessionLaunchDestination() {}
