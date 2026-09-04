package transport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const sessionReattachAuthorityKeyFile = "session-reattach.key"

var sessionReattachAuthorityFileMu sync.Mutex

type sessionReattachAuthority struct {
	key [sha256.Size]byte
}

func loadSessionReattachAuthority(persistenceRoot string) (*sessionReattachAuthority, error) {
	root := strings.TrimSpace(persistenceRoot)
	if root == "" {
		return nil, errors.New("Session reattach persistence root is required")
	}
	sessionReattachAuthorityFileMu.Lock()
	defer sessionReattachAuthorityFileMu.Unlock()

	path := filepath.Join(root, sessionReattachAuthorityKeyFile)
	key, err := readSessionReattachAuthorityKey(path)
	if err == nil {
		return sessionReattachAuthorityFromKey(key)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create Session reattach authority directory: %w", err)
	}
	authority := &sessionReattachAuthority{}
	if _, err := rand.Read(authority.key[:]); err != nil {
		return nil, fmt.Errorf("generate Session reattach authority: %w", err)
	}
	if err := writeSessionReattachAuthorityKey(path, authority.key[:]); err != nil {
		return nil, err
	}
	return authority, nil
}

func readSessionReattachAuthorityKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Session reattach authority must be a regular file: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("set Session reattach authority permissions: %w", err)
		}
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Session reattach authority: %w", err)
	}
	return key, nil
}

func sessionReattachAuthorityFromKey(key []byte) (*sessionReattachAuthority, error) {
	if len(key) != sha256.Size {
		return nil, fmt.Errorf("Session reattach authority key has %d bytes, want %d", len(key), sha256.Size)
	}
	authority := &sessionReattachAuthority{}
	copy(authority.key[:], key)
	return authority, nil
}

func writeSessionReattachAuthorityKey(path string, key []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create Session reattach authority temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set Session reattach authority temporary permissions: %w", err)
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Session reattach authority: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Session reattach authority: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace Session reattach authority: %w", err)
	}
	committed = true
	return nil
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
