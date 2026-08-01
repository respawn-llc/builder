package worktree

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"core/server/metadata"
	"core/shared/config"
)

const (
	workspacePathKeyMaxBytes = 24
	managedPathComponentMax  = 31
	workspaceParentMarker    = ".kent-workspace-owner"
	workspaceParentMarkerVer = 1
)

var errManagedRootBaseInvalid = errors.New("managed worktree base is not a directory")

type managedRootBase struct {
	path string
	err  error
}

type managedRootAllocator struct {
	metadata       *metadata.Store
	configuredBase string
	base           managedRootBase
	entropy        io.Reader
}

type workspaceParentMarkerData struct {
	Version     int    `json:"version"`
	WorkspaceID string `json:"workspace_id"`
}

type managedRootExhaustionError struct {
	Operation   string
	WorkspaceID string
	Base        string
	Parent      string
	TaskShortID string
	Widths      []int
	Candidates  []string
}

func (e *managedRootExhaustionError) Error() string {
	if e == nil {
		return "managed root allocation exhausted"
	}
	return fmt.Sprintf(
		"managed root allocation exhausted: operation=%s workspace_id=%q base=%q parent=%q task=%q widths=%v candidates=%v",
		e.Operation, e.WorkspaceID, e.Base, e.Parent, e.TaskShortID, e.Widths, e.Candidates,
	)
}

type managedRootReservation struct {
	allocator  *managedRootAllocator
	parent     string
	parentInfo os.FileInfo
	root       string
	info       os.FileInfo
	released   bool
}

func (r *managedRootReservation) validate() error {
	if r == nil || r.allocator == nil {
		return errors.New("managed root reservation is required")
	}
	base, err := r.allocator.automaticBase()
	if err != nil {
		return err
	}
	parentInfo, err := os.Lstat(r.parent)
	if err != nil {
		return fmt.Errorf("revalidate reserved worktree parent %q: %w", r.parent, err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || !os.SameFile(r.parentInfo, parentInfo) {
		return fmt.Errorf("reserved worktree parent %q changed identity or type", r.parent)
	}
	canonicalParent, err := filepath.EvalSymlinks(r.parent)
	if err != nil {
		return fmt.Errorf("resolve reserved worktree parent %q: %w", r.parent, err)
	}
	if err := requireManagedPathContained(base, canonicalParent); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(r.root)
	if err != nil {
		return fmt.Errorf("revalidate reserved worktree root %q: %w", r.root, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || !os.SameFile(r.info, rootInfo) {
		return fmt.Errorf("reserved worktree root %q changed identity or type", r.root)
	}
	canonicalRoot, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		return fmt.Errorf("resolve reserved worktree root %q: %w", r.root, err)
	}
	return requireManagedPathContained(base, canonicalRoot)
}

func (r *managedRootReservation) release() error {
	if r == nil || r.released {
		return nil
	}
	r.released = true
	if err := r.validateParentForCleanup(); err != nil {
		return err
	}
	current, err := os.Lstat(r.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect reserved worktree root %q: %w", r.root, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() {
		return fmt.Errorf("refusing reserved worktree root cleanup for incompatible object %q", r.root)
	}
	if !os.SameFile(r.info, current) {
		return fmt.Errorf("refusing reserved worktree root cleanup after identity changed: %q", r.root)
	}
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return fmt.Errorf("inspect reserved worktree root %q: %w", r.root, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("refusing reserved worktree root cleanup because %q is not empty", r.root)
	}
	if err := os.Remove(r.root); err != nil {
		return fmt.Errorf("remove reserved worktree root %q: %w", r.root, err)
	}
	return nil
}

func (r *managedRootReservation) validateParentForCleanup() error {
	if r == nil || r.allocator == nil {
		return errors.New("managed root reservation is required")
	}
	base, err := r.allocator.automaticBase()
	if err != nil {
		return err
	}
	currentParent, err := os.Lstat(r.parent)
	if err != nil {
		return fmt.Errorf("inspect reserved worktree parent %q: %w", r.parent, err)
	}
	if currentParent.Mode()&os.ModeSymlink != 0 || !currentParent.IsDir() || !os.SameFile(r.parentInfo, currentParent) {
		return fmt.Errorf("refusing reserved worktree cleanup after parent identity changed: %q", r.parent)
	}
	canonicalParent, err := filepath.EvalSymlinks(r.parent)
	if err != nil {
		return fmt.Errorf("resolve reserved worktree parent %q during cleanup: %w", r.parent, err)
	}
	return requireManagedPathContained(base, canonicalParent)
}

func (r *managedRootReservation) exactLeafOccupied(leaf string) (bool, error) {
	if r == nil {
		return false, errors.New("managed root reservation is required")
	}
	trimmed := strings.TrimSpace(leaf)
	if trimmed == "" || filepath.Base(filepath.Clean(trimmed)) != trimmed || filepath.IsAbs(trimmed) {
		return false, errors.New("leaf must be one path component")
	}
	candidate := filepath.Join(r.parent, trimmed)
	if candidate == r.root {
		return false, nil
	}
	_, err := os.Lstat(candidate)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect exact managed root candidate %q: %w", candidate, err)
}

func (r *managedRootReservation) disarm() {
	if r != nil {
		r.released = true
	}
}

func newManagedRootAllocator(store *metadata.Store, baseDir string, entropy io.Reader) *managedRootAllocator {
	if entropy == nil {
		entropy = rand.Reader
	}
	allocator := &managedRootAllocator{
		metadata:       store,
		configuredBase: strings.TrimSpace(baseDir),
		entropy:        entropy,
	}
	allocator.base = initializeManagedRootBase(allocator.configuredBase)
	return allocator
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

func (a *managedRootAllocator) initializedBase() managedRootBase {
	if a == nil {
		return managedRootBase{err: errors.New("managed root allocator is required")}
	}
	return a.base
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

func (a *managedRootAllocator) resolveExplicitRoot(requestedRoot string) (string, error) {
	trimmed := strings.TrimSpace(requestedRoot)
	if trimmed == "" {
		return "", errors.New("requested managed worktree root is required")
	}
	expanded, err := expandTildePath(trimmed)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return config.CanonicalWorkspaceRoot(expanded)
	}
	base, err := a.automaticBase()
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(expanded)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("relative worktree root %q escapes base dir", requestedRoot)
	}
	candidate := filepath.Join(base, cleaned)
	if err := requireManagedPathContained(base, candidate); err != nil {
		return "", err
	}
	return config.CanonicalWorkspaceRoot(candidate)
}

func (a *managedRootAllocator) ensureWorkspaceParent(ctx context.Context, workspaceID string, workspaceRoot string) (string, error) {
	base, err := a.automaticBase()
	if err != nil {
		return "", err
	}
	if a.metadata == nil {
		return "", errors.New("metadata store is required")
	}
	workspace, err := a.metadata.GetWorkspaceByID(ctx, strings.TrimSpace(workspaceID))
	if err != nil {
		return "", fmt.Errorf("get workspace for managed parent: %w", err)
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspace.CanonicalRootPath)
	if err != nil {
		return "", fmt.Errorf("canonicalize persisted workspace root: %w", err)
	}
	callerWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize caller workspace root: %w", err)
	}
	if canonicalWorkspaceRoot != callerWorkspaceRoot {
		return "", fmt.Errorf("workspace root %q does not match persisted canonical root %q", workspaceRoot, workspace.CanonicalRootPath)
	}
	if workspace.ManagedWorktreePathKey.Valid {
		return a.materializePersistedWorkspaceParent(workspace.ID, workspace.ManagedWorktreePathKey.String, base)
	}
	seed := normalizeWorkspacePathKey(filepath.Base(filepath.Clean(canonicalWorkspaceRoot)))
	for width := 0; width <= 4; width++ {
		suffix := ""
		if width > 0 {
			suffix, err = a.randomDecimal(width + 2)
			if err != nil {
				return "", err
			}
		}
		candidate := workspacePathKeyCandidate(seed, suffix)
		parent := filepath.Join(base, candidate)
		exists, err := lstatPathExists(parent)
		if err != nil {
			return "", fmt.Errorf("inspect managed workspace parent candidate %q: %w", parent, err)
		}
		if exists {
			continue
		}
		claimed, err := a.metadata.ClaimWorkspacePathKey(ctx, workspace.ID, candidate)
		if err != nil {
			if errors.Is(err, metadata.ErrWorkspacePathKeyCandidateCollision) {
				continue
			}
			return "", err
		}
		return a.materializeClaimedWorkspaceParent(workspace.ID, claimed, base)
	}
	panic(fmt.Sprintf("operation=workspace-parent workspace_id=%q base=%q seed=%q attempted_widths=direct,3,4,5,6", workspace.ID, base, seed))
}

func (a *managedRootAllocator) reserveRegularRoot(ctx context.Context, workspaceID string, workspaceRoot string) (*managedRootReservation, error) {
	parent, err := a.ensureWorkspaceParent(ctx, workspaceID, workspaceRoot)
	if err != nil {
		return nil, err
	}
	attempted := make([]string, 0, 4)
	for width := 3; width <= 6; width++ {
		leaf, err := a.randomDecimal(width)
		if err != nil {
			return nil, err
		}
		attempted = append(attempted, leaf)
		if reservation, collision, err := a.reserveLeaf(parent, leaf); err != nil {
			return nil, err
		} else if !collision {
			return reservation, nil
		}
	}
	panic(&managedRootExhaustionError{
		Operation:   "regular-leaf",
		WorkspaceID: workspaceID,
		Base:        a.base.path,
		Parent:      parent,
		Widths:      []int{3, 4, 5, 6},
		Candidates:  attempted,
	})
}

func (a *managedRootAllocator) reserveTaskRoot(ctx context.Context, workspaceID string, workspaceRoot string, taskShortID string) (*managedRootReservation, error) {
	parent, err := a.ensureWorkspaceParent(ctx, workspaceID, workspaceRoot)
	if err != nil {
		return nil, err
	}
	leaf := strings.TrimSpace(taskShortID)
	if leaf == "" || filepath.Base(filepath.Clean(leaf)) != leaf || filepath.IsAbs(leaf) {
		return nil, errors.New("task short id must be one path component")
	}
	attempted := []string{leaf}
	if reservation, collision, err := a.reserveLeaf(parent, leaf); err != nil {
		return nil, err
	} else if !collision {
		return reservation, nil
	}
	for width := 3; width <= 6; width++ {
		suffix, err := a.randomDecimal(width)
		if err != nil {
			return nil, err
		}
		candidate := leaf + "-" + suffix
		attempted = append(attempted, candidate)
		if reservation, collision, err := a.reserveLeaf(parent, candidate); err != nil {
			return nil, err
		} else if !collision {
			return reservation, nil
		}
	}
	panic(&managedRootExhaustionError{
		Operation:   "task-leaf",
		WorkspaceID: workspaceID,
		Base:        a.base.path,
		Parent:      parent,
		TaskShortID: taskShortID,
		Widths:      []int{0, 3, 4, 5, 6},
		Candidates:  attempted,
	})
}

func (a *managedRootAllocator) reserveLeaf(parent string, leaf string) (*managedRootReservation, bool, error) {
	root := filepath.Join(parent, leaf)
	if err := requireManagedPathContained(parent, root); err != nil {
		return nil, false, err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, false, fmt.Errorf("stat managed worktree parent %q: %w", parent, err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return nil, false, fmt.Errorf("managed worktree parent %q is not a directory", parent)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("reserve managed worktree root %q: %w", root, err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, false, fmt.Errorf("stat reserved managed worktree root %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, fmt.Errorf("reserved managed worktree root %q is not a directory", root)
	}
	if err := requireManagedPathContained(parent, root); err != nil {
		return nil, false, err
	}
	return &managedRootReservation{allocator: a, parent: parent, parentInfo: parentInfo, root: root, info: info}, false, nil
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

func lstatPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (a *managedRootAllocator) materializePersistedWorkspaceParent(workspaceID string, key string, base string) (string, error) {
	return a.materializeWorkspaceParent(workspaceID, key, base, false)
}

func (a *managedRootAllocator) materializeClaimedWorkspaceParent(workspaceID string, key string, base string) (string, error) {
	return a.materializeWorkspaceParent(workspaceID, key, base, true)
}

func (a *managedRootAllocator) materializeWorkspaceParent(workspaceID string, key string, base string, claimed bool) (root string, err error) {
	parent := filepath.Join(base, key)
	if err := requireManagedPathContained(base, parent); err != nil {
		return "", err
	}
	info, err := os.Lstat(parent)
	createdParent := false
	var capturedParent os.FileInfo
	var capturedMarker os.FileInfo
	cleanup := func() error {
		if !claimed || !createdParent || capturedParent == nil {
			return nil
		}
		return rollbackCreatedWorkspaceParent(parent, workspaceParentMarker, capturedParent, capturedMarker)
	}
	defer func() {
		if err != nil {
			if cleanupErr := cleanup(); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			if claimed && a.metadata != nil {
				if releaseErr := a.metadata.ReleaseWorkspacePathKey(context.Background(), workspaceID, key); releaseErr != nil {
					err = errors.Join(err, releaseErr)
				}
			}
		}
	}()
	if os.IsNotExist(err) {
		if err := os.Mkdir(parent, 0o755); err != nil {
			return "", fmt.Errorf("create managed workspace parent %q: %w", parent, err)
		}
		createdParent = true
		info, err = os.Lstat(parent)
		capturedParent = info
	}
	if err != nil {
		return "", fmt.Errorf("stat managed workspace parent %q: %w", parent, err)
	}
	capturedParent = info
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed workspace parent %q is not a directory", parent)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve managed workspace parent %q: %w", parent, err)
	}
	if err := requireManagedPathContained(base, canonicalParent); err != nil {
		return "", err
	}
	marker := filepath.Join(parent, workspaceParentMarker)
	markerInfo, markerErr := os.Lstat(marker)
	if os.IsNotExist(markerErr) && createdParent {
		file, createErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return "", fmt.Errorf("create workspace parent marker %q: %w", marker, createErr)
		}
		capturedMarker, _ = file.Stat()
		payload, marshalErr := json.Marshal(workspaceParentMarkerData{Version: workspaceParentMarkerVer, WorkspaceID: workspaceID})
		if marshalErr == nil {
			_, marshalErr = file.Write(payload)
		}
		closeErr := file.Close()
		if marshalErr != nil || closeErr != nil {
			return "", fmt.Errorf("write workspace parent marker %q: %w", marker, errors.Join(marshalErr, closeErr))
		}
		markerInfo, markerErr = os.Lstat(marker)
		capturedMarker = markerInfo
	}
	if markerErr != nil {
		return "", fmt.Errorf("inspect workspace parent marker %q: %w", marker, markerErr)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return "", fmt.Errorf("workspace parent marker %q is not a regular file", marker)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		return "", fmt.Errorf("read workspace parent marker %q: %w", marker, err)
	}
	var markerData workspaceParentMarkerData
	if err := json.Unmarshal(raw, &markerData); err != nil {
		return "", fmt.Errorf("decode workspace parent marker %q: %w", marker, err)
	}
	if markerData.Version != workspaceParentMarkerVer || markerData.WorkspaceID != workspaceID {
		return "", fmt.Errorf("workspace parent marker %q ownership/version mismatch", marker)
	}
	finalParent, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("revalidate workspace parent %q: %w", parent, err)
	}
	if finalParent.Mode()&os.ModeSymlink != 0 || !finalParent.IsDir() || !os.SameFile(capturedParent, finalParent) {
		return "", fmt.Errorf("workspace parent %q changed identity or type", parent)
	}
	finalCanonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve revalidated workspace parent %q: %w", parent, err)
	}
	if err := requireManagedPathContained(base, finalCanonicalParent); err != nil {
		return "", err
	}
	finalMarker, err := os.Lstat(marker)
	if err != nil {
		return "", fmt.Errorf("revalidate workspace parent marker %q: %w", marker, err)
	}
	if finalMarker.Mode()&os.ModeSymlink != 0 || !finalMarker.Mode().IsRegular() || !os.SameFile(markerInfo, finalMarker) {
		return "", fmt.Errorf("workspace parent marker %q changed identity or type", marker)
	}
	finalRaw, err := os.ReadFile(marker)
	if err != nil {
		return "", fmt.Errorf("read revalidated workspace parent marker %q: %w", marker, err)
	}
	var finalMarkerData workspaceParentMarkerData
	if err := json.Unmarshal(finalRaw, &finalMarkerData); err != nil {
		return "", fmt.Errorf("decode revalidated workspace parent marker %q: %w", marker, err)
	}
	if finalMarkerData.Version != workspaceParentMarkerVer || finalMarkerData.WorkspaceID != workspaceID {
		return "", fmt.Errorf("workspace parent marker %q ownership/version mismatch after revalidation", marker)
	}
	return filepath.Clean(parent), nil
}

func rollbackCreatedWorkspaceParent(parent string, markerName string, parentInfo os.FileInfo, markerInfo os.FileInfo) error {
	currentParent, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect parent during cleanup %q: %w", parent, err)
	}
	if !os.SameFile(parentInfo, currentParent) || currentParent.Mode()&os.ModeSymlink != 0 || !currentParent.IsDir() {
		return fmt.Errorf("refusing parent cleanup after identity changed: %q", parent)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("read parent during cleanup %q: %w", parent, err)
	}
	if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != markerName) {
		return fmt.Errorf("refusing parent cleanup because %q is not marker-only", parent)
	}
	if markerInfo != nil {
		currentMarker, err := os.Lstat(filepath.Join(parent, markerName))
		if err != nil {
			return fmt.Errorf("inspect marker during cleanup %q: %w", parent, err)
		}
		if !os.SameFile(markerInfo, currentMarker) {
			return fmt.Errorf("refusing marker cleanup after identity changed: %q", parent)
		}
		if err := os.Remove(filepath.Join(parent, markerName)); err != nil {
			return fmt.Errorf("remove marker during cleanup %q: %w", parent, err)
		}
	}
	if err := os.Remove(parent); err != nil {
		return fmt.Errorf("remove parent during cleanup %q: %w", parent, err)
	}
	return nil
}

func requireManagedPathContained(base string, candidate string) error {
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return fmt.Errorf("resolve managed worktree base %q: %w", base, err)
	}
	canonicalCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("resolve managed worktree candidate %q: %w", candidate, err)
		}
		absoluteCandidate, absErr := filepath.Abs(filepath.Clean(candidate))
		if absErr != nil {
			return fmt.Errorf("resolve managed worktree candidate %q: %w", candidate, absErr)
		}
		if err := requireRelativeContained(canonicalBase, absoluteCandidate); err != nil {
			return err
		}
		return nil
	}
	return requireRelativeContained(canonicalBase, canonicalCandidate)
}

func requireRelativeContained(base string, candidate string) error {
	rel, err := filepath.Rel(base, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("managed worktree path %q escapes base %q", candidate, base)
	}
	return nil
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

func workspacePathKeyCandidate(parentKey string, suffix string) string {
	parentKey = normalizeWorkspacePathKey(parentKey)
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return parentKey
	}
	return parentKey + "-" + suffix
}
