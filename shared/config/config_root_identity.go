package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// canonicalRootForIdentity returns the stable comparison/hash key for a
// persistence root. It cleans separators and, on platforms whose default
// filesystem is case-insensitive (macOS, Windows), folds case so the same
// directory spelled with different casing for the server and client resolves to
// the same identity instead of a false ErrServerRootMismatch. Case-sensitive
// platforms (Linux) preserve the caller's spelling. The rare case-sensitive
// volume on darwin/windows trades a possible hash collision (fail-open: accept)
// for never falsely rejecting the same directory, which is the safer default for
// an isolation identity check.
func canonicalRootForIdentity(root string) string {
	cleaned := filepath.Clean(root)
	switch runtime.GOOS {
	case "darwin", "windows":
		return strings.ToLower(cleaned)
	default:
		return cleaned
	}
}

// CanonicalPathIdentity returns the stable comparison key used for path
// identity checks that must follow persistence-root case-folding semantics.
func CanonicalPathIdentity(path string) (string, error) {
	real, err := ResolveExistingAncestorRealPath(path)
	if err != nil {
		return "", err
	}
	return canonicalRootForIdentity(real), nil
}

// CanonicalLexicalPathIdentity returns a stable comparison key for the caller's
// spelling of a path without following symlinks in existing ancestors.
func CanonicalLexicalPathIdentity(path string) (string, error) {
	absolute, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
	return canonicalRootForIdentity(absolute), nil
}

// ResolveExistingPathRealPath resolves a path that must already exist to its
// symlink-evaluated, cleaned, absolute filesystem path.
func ResolveExistingPathRealPath(path string) (string, error) {
	absolute, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(real), nil
	} else {
		return "", fmt.Errorf("resolve real path for %q: %w", absolute, err)
	}
}

// ResolveExistingAncestorRealPath resolves a path to the same identity as the
// existing filesystem object at that path, or to the nearest existing
// symlink-evaluated ancestor plus the missing tail. This is the shared write
// target identity used by tool path guards and path deny policies.
func ResolveExistingAncestorRealPath(path string) (string, error) {
	absolute, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(real), nil
	} else if !isMissingPathForAncestorResolution(err) {
		return "", fmt.Errorf("resolve real path for %q: %w", absolute, err)
	}

	parent := filepath.Dir(absolute)
	for {
		if _, err := os.Stat(parent); err == nil {
			parentReal, evalErr := filepath.EvalSymlinks(parent)
			if evalErr != nil {
				return "", fmt.Errorf("resolve real path ancestor for %q: %w", absolute, evalErr)
			}
			rel, relErr := filepath.Rel(parent, absolute)
			if relErr != nil {
				return "", fmt.Errorf("resolve real path tail for %q: %w", absolute, relErr)
			}
			return filepath.Clean(filepath.Join(parentReal, rel)), nil
		} else if !isMissingPathForAncestorResolution(err) {
			return "", fmt.Errorf("stat real path ancestor for %q: %w", absolute, err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("resolve real path for %q: no existing ancestor", absolute)
		}
		parent = next
	}
}

func isMissingPathForAncestorResolution(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func absoluteCleanPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path for %q: %w", path, err)
	}
	return filepath.Clean(absolute), nil
}

// PersistenceRootHash returns a short, stable identifier for a persistence
// root. It is derived from the canonicalized root path (see
// canonicalRootForIdentity) and is used both to scope the local RPC socket
// directory and to stamp protocol.ServerIdentity so clients can confirm an
// attached server actually serves the requested root rather than a different
// instance reachable on the same TCP endpoint.
//
// Client and server must derive the hash from the same absolute root for the
// comparison to hold; both use the root resolved by config.Load. Casing is
// folded on case-insensitive default filesystems so the same directory spelled
// differently does not produce diverging ids.
func PersistenceRootHash(persistenceRoot string) string {
	trimmedRoot := strings.TrimSpace(persistenceRoot)
	if trimmedRoot == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(canonicalRootForIdentity(trimmedRoot)))
	return hex.EncodeToString(hash[:8])
}

// ExplicitPersistenceRootID returns the persistence-root id an attached server
// must report when the operator explicitly selected a non-default root (via the
// --persistence-root flag or KENT_PERSISTENCE_ROOT). It returns "" when the root
// was not explicitly selected, or when an explicit root resolves to the default
// (<home>/.kent) — both leave attach behavior unchanged and stay compatible with
// older servers that report an empty id. When the default-root comparison cannot
// be resolved (for example HOME is unset in a stripped environment), the explicit
// root stays pinned rather than silently disabling the check, so an isolated-root
// client never falls back to a different server on the same TCP endpoint. The
// source label is set by config.Load (see resolveConfigRoot): "default", "flag",
// or "env".
func ExplicitPersistenceRootID(cfg App) string {
	switch cfg.Source.Sources["persistence_root"] {
	case "flag", "env":
		if isDefault, err := IsDefaultPersistenceRoot(cfg.PersistenceRoot); err == nil && isDefault {
			return ""
		}
		return PersistenceRootHash(cfg.PersistenceRoot)
	default:
		return ""
	}
}

// IsDefaultPersistenceRoot reports whether the supplied root resolves to the
// default persistence root (<home>/.kent). An explicit --persistence-root or
// KENT_PERSISTENCE_ROOT that points at the default carries no cross-root
// isolation risk, so callers skip root-id attach validation for it and stay
// compatible with servers that predate persistence-root identity stamping
// (which report an empty id). An empty root is treated as the default.
func IsDefaultPersistenceRoot(persistenceRoot string) (bool, error) {
	trimmed := strings.TrimSpace(persistenceRoot)
	if trimmed == "" {
		return true, nil
	}
	defaultRoot, err := NormalizePersistenceRoot(DefaultPersistence)
	if err != nil {
		return false, err
	}
	resolved, err := NormalizePersistenceRoot(trimmed)
	if err != nil {
		return false, err
	}
	return canonicalRootForIdentity(resolved) == canonicalRootForIdentity(defaultRoot), nil
}
