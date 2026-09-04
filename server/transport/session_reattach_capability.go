package transport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

type sessionReattachAuthority struct {
	key [sha256.Size]byte
}

func newSessionReattachAuthority() (*sessionReattachAuthority, error) {
	authority := &sessionReattachAuthority{}
	if _, err := rand.Read(authority.key[:]); err != nil {
		return nil, fmt.Errorf("generate Session reattach authority: %w", err)
	}
	return authority, nil
}

func (a *sessionReattachAuthority) issue(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if a == nil || sessionID == "" {
		return "", errors.New("Session reattach authority and Session ID are required")
	}
	mac := hmac.New(sha256.New, a.key[:])
	_, _ = mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *sessionReattachAuthority) authorizes(sessionID string, capability *string) bool {
	if a == nil || capability == nil {
		return false
	}
	expected, err := a.issue(sessionID)
	if err != nil {
		return false
	}
	actual := strings.TrimSpace(*capability)
	return actual == *capability && hmac.Equal([]byte(actual), []byte(expected))
}
