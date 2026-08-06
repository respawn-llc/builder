package worktree

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"core/shared/config"
)

const workspacePathKeyMaxBytes = 24

var errManagedRootBaseInvalid = errors.New("managed worktree base is not a directory")
var errManagedRootOverlapsSourceWorkspace = errors.New("automatic managed worktree root overlaps source workspace")

type managedRootBase struct {
	path string
	err  error
}

type managedRootAllocator struct {
	base    managedRootBase
	entropy io.Reader
}

type managedRootExhaustionError struct {
	Operation   string
	Base        string
	Parent      string
	TaskShortID *string
	Candidates  []string
}

func (e *managedRootExhaustionError) Error() string {
	if e == nil {
		return "managed root allocation exhausted"
	}
	taskShortID := "<absent>"
	if e.TaskShortID != nil {
		taskShortID = fmt.Sprintf("%q", *e.TaskShortID)
	}
	return fmt.Sprintf(
		"managed root allocation exhausted: operation=%s base=%q parent=%q task=%s candidates=%v",
		e.Operation,
		e.Base,
		e.Parent,
		taskShortID,
		e.Candidates,
	)
}

func newManagedRootAllocator(baseDir string, entropy io.Reader) *managedRootAllocator {
	if entropy == nil {
		entropy = rand.Reader
	}
	return &managedRootAllocator{
		base:    initializeManagedRootBase(strings.TrimSpace(baseDir)),
		entropy: entropy,
	}
}

func initializeManagedRootBase(configuredBase string) managedRootBase {
	expanded, err := expandTildePath(configuredBase)
	if err != nil {
		return managedRootBase{err: err}
	}
	if strings.TrimSpace(expanded) == "" {
		return managedRootBase{err: errors.New("worktree base dir is required")}
	}
	info, err := os.Lstat(expanded)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(expanded, 0o755); err != nil {
			return managedRootBase{err: fmt.Errorf("create managed worktree base %q: %w", expanded, err)}
		}
		info, err = os.Lstat(expanded)
	}
	if err != nil {
		return managedRootBase{err: fmt.Errorf("stat managed worktree base %q: %w", expanded, err)}
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return managedRootBase{err: fmt.Errorf("%q: %w", expanded, errManagedRootBaseInvalid)}
	}
	canonical, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return managedRootBase{err: fmt.Errorf("resolve managed worktree base %q: %w", expanded, err)}
	}
	targetInfo, err := os.Lstat(canonical)
	if err != nil {
		return managedRootBase{err: fmt.Errorf("stat resolved managed worktree base %q: %w", canonical, err)}
	}
	if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return managedRootBase{err: fmt.Errorf("%q: %w", expanded, errManagedRootBaseInvalid)}
	}
	return managedRootBase{path: filepath.Clean(canonical)}
}

func (a *managedRootAllocator) automaticBase() (string, error) {
	if a == nil {
		return "", errors.New("managed root allocator is required")
	}
	if a.base.err != nil {
		return "", a.base.err
	}
	return a.base.path, nil
}

func (a *managedRootAllocator) resolveExplicitRoot(requestedRoot string, sourceWorkspaceRoot string) (string, error) {
	trimmed := strings.TrimSpace(requestedRoot)
	if trimmed == "" {
		return "", errors.New("requested managed worktree root is required")
	}
	expanded, err := expandTildePath(trimmed)
	if err != nil {
		return "", err
	}
	base, err := a.automaticBase()
	if err != nil {
		return "", err
	}
	var candidate string
	if filepath.IsAbs(expanded) {
		candidate = expanded
	} else {
		cleaned := filepath.Clean(expanded)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("relative worktree root %q escapes base dir", requestedRoot)
		}
		candidate = filepath.Join(base, cleaned)
	}
	resolved, err := config.ResolveExistingAncestorRealPath(candidate)
	if err != nil {
		return "", err
	}
	if !sameOrDescendantPath(base, resolved) {
		return "", fmt.Errorf("managed worktree root %q is outside base %q", requestedRoot, base)
	}
	source, err := config.CanonicalWorkspaceRoot(sourceWorkspaceRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize source workspace root: %w", err)
	}
	if sameOrDescendantPath(source, resolved) || sameOrDescendantPath(resolved, source) {
		return "", fmt.Errorf("managed worktree root %q overlaps source workspace %q", requestedRoot, source)
	}
	return resolved, nil
}

func (a *managedRootAllocator) ensureWorkspaceParent(workspaceRoot string) (string, error) {
	base, err := a.automaticBase()
	if err != nil {
		return "", err
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize source workspace root: %w", err)
	}
	parent := filepath.Join(base, normalizeWorkspacePathKey(filepath.Base(canonicalWorkspaceRoot)))
	if !sameOrDescendantPath(base, parent) {
		return "", fmt.Errorf("managed workspace parent %q escapes base %q", parent, base)
	}
	if sameOrDescendantPath(canonicalWorkspaceRoot, parent) {
		return "", fmt.Errorf(
			"managed workspace parent %q overlaps source workspace %q: %w",
			parent,
			canonicalWorkspaceRoot,
			errManagedRootOverlapsSourceWorkspace,
		)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create managed workspace parent %q: %w", parent, err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("stat managed workspace parent %q: %w", parent, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed workspace parent %q is not a directory", parent)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve managed workspace parent %q: %w", parent, err)
	}
	if !sameOrDescendantPath(base, canonicalParent) {
		return "", fmt.Errorf("managed workspace parent %q escapes base %q", parent, base)
	}
	return filepath.Clean(parent), nil
}

func (a *managedRootAllocator) reserveRegularRoot(workspaceRoot string) (string, error) {
	parent, err := a.ensureWorkspaceParent(workspaceRoot)
	if err != nil {
		return "", err
	}
	attempted := make([]string, 0, 4)
	for width := 3; width <= 6; width++ {
		leaf, err := a.randomDecimal(width)
		if err != nil {
			return "", err
		}
		attempted = append(attempted, leaf)
		if root, collision, err := reserveManagedLeaf(parent, leaf); err != nil {
			return "", err
		} else if !collision {
			return root, nil
		}
	}
	panic(&managedRootExhaustionError{
		Operation:  "regular-leaf",
		Base:       a.base.path,
		Parent:     parent,
		Candidates: attempted,
	})
}

func (a *managedRootAllocator) reserveTaskRoot(workspaceRoot string, taskShortID string) (string, error) {
	parent, err := a.ensureWorkspaceParent(workspaceRoot)
	if err != nil {
		return "", err
	}
	leaf := strings.TrimSpace(taskShortID)
	if leaf == "" || filepath.Base(filepath.Clean(leaf)) != leaf || filepath.IsAbs(leaf) {
		return "", errors.New("task short id must be one path component")
	}
	attempted := []string{leaf}
	if root, collision, err := reserveManagedLeaf(parent, leaf); err != nil {
		return "", err
	} else if !collision {
		return root, nil
	}
	for width := 3; width <= 6; width++ {
		suffix, err := a.randomDecimal(width)
		if err != nil {
			return "", err
		}
		candidate := leaf + "-" + suffix
		attempted = append(attempted, candidate)
		if root, collision, err := reserveManagedLeaf(parent, candidate); err != nil {
			return "", err
		} else if !collision {
			return root, nil
		}
	}
	panic(&managedRootExhaustionError{
		Operation:   "task-leaf",
		Base:        a.base.path,
		Parent:      parent,
		TaskShortID: &leaf,
		Candidates:  attempted,
	})
}

func (a *managedRootAllocator) exactTaskRootOccupied(workspaceRoot string, taskShortID string) (bool, error) {
	parent, err := a.ensureWorkspaceParent(workspaceRoot)
	if err != nil {
		return false, err
	}
	leaf := strings.TrimSpace(taskShortID)
	if leaf == "" || filepath.Base(filepath.Clean(leaf)) != leaf || filepath.IsAbs(leaf) {
		return false, errors.New("task short id must be one path component")
	}
	_, err = os.Lstat(filepath.Join(parent, leaf))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect exact managed task root: %w", err)
}

func reserveManagedLeaf(parent string, leaf string) (string, bool, error) {
	root := filepath.Join(parent, leaf)
	if !sameOrDescendantPath(parent, root) {
		return "", false, fmt.Errorf("managed worktree root %q escapes parent %q", root, parent)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", true, nil
		}
		return "", false, fmt.Errorf("reserve managed worktree root %q: %w", root, err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", false, fmt.Errorf("stat reserved managed worktree root %q: %w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("reserved managed worktree root %q is not a directory", root)
	}
	return root, false, nil
}

func removeEmptyManagedRootAfterAddFailure(root string) error {
	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove empty reserved managed worktree root %q after Git add failure: %w", root, err)
	}
	return nil
}

func (a *managedRootAllocator) randomDecimal(width int) (string, error) {
	if width < 1 {
		return "", errors.New("random decimal width must be positive")
	}
	buf := make([]byte, width)
	if _, err := io.ReadFull(a.entropy, buf); err != nil {
		return "", fmt.Errorf("read managed worktree path entropy: %w", err)
	}
	for i := range buf {
		buf[i] = '0' + (buf[i] % 10)
	}
	return string(buf), nil
}

func normalizeWorkspacePathKey(sourceFolder string) string {
	value := strings.TrimSpace(sourceFolder)
	var builder strings.Builder
	separatorPending := false
	for _, r := range value {
		if r <= unicode.MaxASCII && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			if separatorPending && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteByte(byte(unicode.ToLower(r)))
			separatorPending = false
			continue
		}
		separatorPending = builder.Len() > 0
	}
	normalized := strings.Trim(builder.String(), "-")
	if len(normalized) > workspacePathKeyMaxBytes {
		normalized = strings.TrimRight(normalized[:workspacePathKeyMaxBytes], "-")
	}
	if normalized == "" || isWindowsReservedPathKey(normalized) {
		return "workspace"
	}
	return normalized
}

func isWindowsReservedPathKey(value string) bool {
	switch strings.ToLower(strings.TrimSuffix(value, ".")) {
	case "con", "prn", "aux", "nul", "clock$":
		return true
	}
	lower := strings.ToLower(value)
	if len(lower) == 4 && (strings.HasPrefix(lower, "com") || strings.HasPrefix(lower, "lpt")) {
		return lower[3] >= '1' && lower[3] <= '9'
	}
	return false
}
