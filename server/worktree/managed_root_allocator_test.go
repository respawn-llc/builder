package worktree

import (
	"bytes"
	"core/server/metadata"
	"core/shared/config"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeWorkspacePathKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "portable folder", in: "Builder CLI", want: "builder-cli"},
		{name: "collapse separators", in: "A___B...C", want: "a-b-c"},
		{name: "non ASCII is not a path component", in: "Café 東京", want: "caf"},
		{name: "reserved name", in: "CON", want: "workspace"},
		{name: "reserved name with suffix", in: "COM1", want: "workspace"},
		{name: "fallback", in: "___", want: "workspace"},
		{name: "truncates at 24 bytes", in: "abcdefghijklmnopqrstuvwxYZ", want: "abcdefghijklmnopqrstuvwx"},
		{name: "trims truncation separator", in: "abcdefghijklmnopqrstuvw xyz", want: "abcdefghijklmnopqrstuvw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWorkspacePathKey(tt.in); got != tt.want {
				t.Fatalf("normalizeWorkspacePathKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWorkspacePathKeyCandidateFitsPortableComponentLimit(t *testing.T) {
	suffixes := []*string{nil}
	for _, value := range []string{"123", "1234", "12345", "123456"} {
		value := value
		suffixes = append(suffixes, &value)
	}
	for _, suffix := range suffixes {
		got := workspacePathKeyCandidate("abcdefghijklmnopqrstuvwx", suffix)
		if len(got) > 31 {
			t.Fatalf("candidate %q is %d bytes, want at most 31", got, len(got))
		}
	}
	suffix := "042"
	if got := workspacePathKeyCandidate("builder", &suffix); got != "builder-042" {
		t.Fatalf("suffixed candidate = %q, want builder-042", got)
	}
}

func TestWorkspacePathKeyCandidateRejectsBlankPresentSuffix(t *testing.T) {
	suffix := " "
	defer func() {
		if recover() == nil {
			t.Fatal("workspacePathKeyCandidate accepted a blank present suffix")
		}
	}()
	_ = workspacePathKeyCandidate("builder", &suffix)
}

func TestManagedRootAllocatorEagerlyInitializesMissingBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "nested", "worktrees")
	allocator := newManagedRootAllocator(nil, base, strings.NewReader("entropy"))
	if allocator.base.err != nil {
		t.Fatalf("allocator base initialization error: %v", allocator.base.err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("missing base was not created: %v", err)
	}
}

func TestManagedRootAllocatorCachesInvalidBaseInitialization(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(base, nil, 0o600); err != nil {
		t.Fatalf("write invalid base: %v", err)
	}
	allocator := newManagedRootAllocator(nil, base, strings.NewReader("entropy"))
	if allocator.base.err == nil {
		t.Fatal("expected invalid base initialization error")
	}
	if !errors.Is(allocator.base.err, errManagedRootBaseInvalid) {
		t.Fatalf("base error = %v, want invalid base sentinel", allocator.base.err)
	}
	if allocator.base.err != allocator.initializedBase().err {
		t.Fatal("base initialization error was not cached")
	}
}

func TestManagedRootAllocatorResolvesConfiguredBaseSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "worktrees-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink configured base: %v", err)
	}
	allocator := newManagedRootAllocator(nil, link, strings.NewReader("entropy"))
	if allocator.base.err != nil {
		t.Fatalf("allocator base initialization error: %v", allocator.base.err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if allocator.base.path != canonicalTarget {
		t.Fatalf("resolved base = %q, want %q", allocator.base.path, canonicalTarget)
	}
}

func TestManagedRootAllocatorClaimsAndMaterializesWorkspaceParent(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	parent, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure workspace parent: %v", err)
	}
	canonicalBase, err := filepath.EvalSymlinks(env.baseDir)
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	if filepath.Dir(parent) != canonicalBase {
		t.Fatalf("workspace parent = %q, want below %q", parent, canonicalBase)
	}
	workspace, err := env.store.GetWorkspaceByID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if !workspace.ManagedWorktreePathKey.Valid || filepath.Base(parent) != workspace.ManagedWorktreePathKey.String {
		t.Fatalf("workspace path key = %v, parent = %q", workspace.ManagedWorktreePathKey, parent)
	}
	marker, err := os.ReadFile(filepath.Join(parent, workspaceParentMarker))
	if err != nil {
		t.Fatalf("read workspace parent marker: %v", err)
	}
	var markerData workspaceParentMarkerData
	if err := json.Unmarshal(marker, &markerData); err != nil {
		t.Fatalf("decode workspace parent marker: %v", err)
	}
	if markerData.Version != workspaceParentMarkerVer || markerData.WorkspaceID != env.binding.WorkspaceID {
		t.Fatalf("workspace parent marker = %+v, want version %d and workspace %q", markerData, workspaceParentMarkerVer, env.binding.WorkspaceID)
	}
}

func TestManagedRootAllocatorRestartsAfterClaimBeforeMkdir(t *testing.T) {
	env := newServiceTestEnv(t)
	if _, err := env.store.ClaimWorkspacePathKey(env.ctx, env.binding.WorkspaceID, "restart"); err != nil {
		t.Fatalf("claim workspace key: %v", err)
	}
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	parent, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("materialize claimed parent after restart: %v", err)
	}
	if filepath.Base(parent) != "restart" {
		t.Fatalf("parent = %q, want restart", parent)
	}
}

func TestManagedRootAllocatorRejectsUnmarkedPersistedParent(t *testing.T) {
	env := newServiceTestEnv(t)
	if _, err := env.store.ClaimWorkspacePathKey(env.ctx, env.binding.WorkspaceID, "occupied"); err != nil {
		t.Fatalf("claim workspace key: %v", err)
	}
	parent := filepath.Join(env.baseDir, "occupied")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("create occupied parent: %v", err)
	}
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	if _, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot); err == nil {
		t.Fatal("accepted unmarked persisted parent")
	}
}

func TestManagedRootAllocatorMetadataFailureDoesNotTouchCandidate(t *testing.T) {
	env := newServiceTestEnv(t)
	closed := env.store
	if err := closed.Close(); err != nil {
		t.Fatalf("close metadata store: %v", err)
	}
	allocator := newManagedRootAllocator(closed, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	candidate := filepath.Join(env.baseDir, normalizeWorkspacePathKey(filepath.Base(env.workspaceRoot)))
	if _, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot); err == nil {
		t.Fatal("metadata failure unexpectedly succeeded")
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate after metadata failure: %v", err)
	}
}

func TestManagedRootAllocatorConcurrentWorkspacesUseDistinctParents(t *testing.T) {
	env := newServiceTestEnv(t)
	rootA := filepath.Join(t.TempDir(), "source")
	rootB := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatalf("create root A: %v", err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatalf("create root B: %v", err)
	}
	bindingA, err := env.store.AttachWorkspaceToProject(env.ctx, env.binding.ProjectID, rootA)
	if err != nil {
		t.Fatalf("attach workspace A: %v", err)
	}
	bindingB, err := env.store.AttachWorkspaceToProject(env.ctx, env.binding.ProjectID, rootB)
	if err != nil {
		t.Fatalf("attach workspace B: %v", err)
	}
	type result struct {
		key string
		err error
	}
	results := make(chan result, 2)
	for _, binding := range []metadata.Binding{bindingA, bindingB} {
		go func(binding metadata.Binding) {
			allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
			parent, err := allocator.ensureWorkspaceParent(env.ctx, binding.WorkspaceID, binding.CanonicalRoot)
			results <- result{key: filepath.Base(parent), err: err}
		}(binding)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent parent allocation errors: %v, %v", first.err, second.err)
	}
	if first.key == second.key {
		t.Fatalf("concurrent allocations returned same parent %q", first.key)
	}
}

func TestManagedRootAllocatorConcurrentlyMaterializesPersistedWorkspaceParent(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	const workers = 32
	start := make(chan struct{})
	results := make(chan struct {
		root string
		err  error
	}, workers)
	for range workers {
		go func() {
			<-start
			root, err := allocator.materializePersistedWorkspaceParent(
				env.binding.WorkspaceID,
				"persisted",
				allocator.base.path,
			)
			results <- struct {
				root string
				err  error
			}{root: root, err: err}
		}()
	}
	close(start)
	var expectedRoot string
	for range workers {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent persisted parent materialization: %v", result.err)
		}
		if expectedRoot == "" {
			expectedRoot = result.root
		} else if result.root != expectedRoot {
			t.Fatalf("concurrent persisted parent roots differ: %q and %q", expectedRoot, result.root)
		}
	}
	entries, err := os.ReadDir(expectedRoot)
	if err != nil {
		t.Fatalf("read concurrently materialized parent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != workspaceParentMarker {
		t.Fatalf("concurrently materialized parent entries = %+v, want only %q", entries, workspaceParentMarker)
	}
}

func TestManagedRootAllocatorRejectsInvalidPersistedMarkers(t *testing.T) {
	env := newServiceTestEnv(t)
	tests := []struct {
		name    string
		payload string
	}{
		{name: "malformed", payload: "{"},
		{name: "partial", payload: `{"version":1`},
		{name: "unknown version", payload: `{"version":2,"workspace_id":"` + env.binding.WorkspaceID + `"}`},
		{name: "wrong owner", payload: `{"version":1,"workspace_id":"other"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "invalid-" + strings.ReplaceAll(tt.name, " ", "-")
			if _, err := env.store.ClaimWorkspacePathKey(env.ctx, env.binding.WorkspaceID, key); err != nil {
				t.Fatalf("claim workspace key: %v", err)
			}
			parent := filepath.Join(env.baseDir, key)
			if err := os.Mkdir(parent, 0o755); err != nil {
				t.Fatalf("create parent: %v", err)
			}
			marker := filepath.Join(parent, workspaceParentMarker)
			if err := os.WriteFile(marker, []byte(tt.payload), 0o600); err != nil {
				t.Fatalf("write marker: %v", err)
			}
			allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
			if _, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot); err == nil {
				t.Fatal("accepted invalid persisted marker")
			}
			if _, err := os.Lstat(parent); err != nil {
				t.Fatalf("invalid marker parent was changed: %v", err)
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != tt.payload {
				t.Fatalf("invalid marker changed: got %q err=%v", got, err)
			}
			if err := env.store.ReleaseWorkspacePathKey(env.ctx, env.binding.WorkspaceID, key); err != nil {
				t.Fatalf("release workspace key: %v", err)
			}
			if err := os.RemoveAll(parent); err != nil {
				t.Fatalf("remove invalid parent: %v", err)
			}
		})
	}
}

func TestRollbackCreatedWorkspaceParentRefusesReplacementIdentity(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	marker := filepath.Join(parent, workspaceParentMarker)
	if err := os.WriteFile(marker, []byte(`{"version":1,"workspace_id":"workspace-1"}`), 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		t.Fatalf("capture parent: %v", err)
	}
	markerInfo, err := os.Lstat(marker)
	if err != nil {
		t.Fatalf("capture marker: %v", err)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.Rename(parent, replacement); err != nil {
		t.Fatalf("move captured parent: %v", err)
	}
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("create replacement parent: %v", err)
	}
	if err := rollbackCreatedWorkspaceParent(parent, workspaceParentMarker, parentInfo, markerInfo); err == nil {
		t.Fatal("rollback removed replacement parent")
	}
	if _, err := os.Lstat(parent); err != nil {
		t.Fatalf("replacement parent missing after refused rollback: %v", err)
	}
}

func TestManagedRootAllocatorReservesRegularLeaf(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	reservation, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("reserve regular root: %v", err)
	}
	if filepath.Base(reservation.root) != "444" {
		t.Fatalf("regular leaf = %q, want 444", filepath.Base(reservation.root))
	}
	if err := reservation.release(); err != nil {
		t.Fatalf("release regular reservation: %v", err)
	}
	if _, err := os.Lstat(reservation.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released regular reservation still exists: %v", err)
	}
}

func TestManagedRootAllocatorReservesTaskShortIDAndEscalates(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	parent, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure parent: %v", err)
	}
	if err := os.Mkdir(filepath.Join(parent, "KENT-335"), 0o755); err != nil {
		t.Fatalf("occupy task leaf: %v", err)
	}
	reservation, err := allocator.reserveTaskRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot, "KENT-335")
	if err != nil {
		t.Fatalf("reserve task root: %v", err)
	}
	if filepath.Base(reservation.root) != "KENT-335-444" {
		t.Fatalf("task leaf = %q, want KENT-335-444", filepath.Base(reservation.root))
	}
	if err := reservation.release(); err != nil {
		t.Fatalf("release task reservation: %v", err)
	}
}

func TestManagedRootAllocatorLeafCollisionWidthsAndIdentitySafeRelease(t *testing.T) {
	env := newServiceTestEnv(t)
	entropy := bytes.NewReader([]byte{1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 3, 4, 4, 4, 4, 4, 4})
	allocator := newManagedRootAllocator(env.store, env.baseDir, entropy)
	parent, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure parent: %v", err)
	}
	for _, leaf := range []string{"KENT-335", "KENT-335-111", "KENT-335-2222"} {
		if err := os.Mkdir(filepath.Join(parent, leaf), 0o755); err != nil {
			t.Fatalf("occupy leaf %q: %v", leaf, err)
		}
	}
	reservation, err := allocator.reserveTaskRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot, "KENT-335")
	if err != nil {
		t.Fatalf("reserve task root: %v", err)
	}
	if filepath.Base(reservation.root) != "KENT-335-33333" {
		t.Fatalf("escalated task leaf = %q, want KENT-335-33333", filepath.Base(reservation.root))
	}
	if err := os.WriteFile(filepath.Join(reservation.root, "modified"), []byte("dirty"), 0o600); err != nil {
		t.Fatalf("dirty reservation: %v", err)
	}
	if err := reservation.release(); err == nil {
		t.Fatal("released non-empty reservation")
	}
	if _, err := os.Lstat(reservation.root); err != nil {
		t.Fatalf("non-empty reservation removed: %v", err)
	}
}

func TestManagedRootAllocatorPanicsAfterSixDigitCollision(t *testing.T) {
	env := newServiceTestEnv(t)
	entropy := bytes.NewReader(bytes.Repeat([]byte{1}, 18))
	allocator := newManagedRootAllocator(env.store, env.baseDir, entropy)
	parent, err := allocator.ensureWorkspaceParent(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("ensure parent: %v", err)
	}
	for _, leaf := range []string{"KENT-335", "KENT-335-111", "KENT-335-1111", "KENT-335-11111", "KENT-335-111111"} {
		if err := os.Mkdir(filepath.Join(parent, leaf), 0o755); err != nil {
			t.Fatalf("occupy leaf %q: %v", leaf, err)
		}
	}
	defer func() {
		value := recover()
		if value == nil {
			t.Fatal("six-digit collision did not panic")
		}
		exhaustion, ok := value.(*managedRootExhaustionError)
		if !ok {
			t.Fatalf("panic type = %T, want *managedRootExhaustionError", value)
		}
		if exhaustion.Operation != "task-leaf" ||
			exhaustion.WorkspaceID != env.binding.WorkspaceID ||
			exhaustion.Base != allocator.base.path ||
			exhaustion.Parent != parent ||
			exhaustion.TaskShortID != "KENT-335" ||
			!slices.Equal(exhaustion.Widths, []int{0, 3, 4, 5, 6}) ||
			!slices.Equal(exhaustion.Candidates, []string{"KENT-335", "KENT-335-111", "KENT-335-1111", "KENT-335-11111", "KENT-335-111111"}) {
			t.Fatalf("panic diagnostics = %+v", exhaustion)
		}
	}()
	_, _ = allocator.reserveTaskRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot, "KENT-335")
}

func TestManagedRootAllocatorReturnsEntropyFailure(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, errorReader{})
	if _, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot); err == nil {
		t.Fatal("entropy failure was swallowed")
	}
}

func TestManagedRootReservationRevalidatesParentAndLeafBeforeGit(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	reservation, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("reserve root: %v", err)
	}
	parent := filepath.Dir(reservation.root)
	backup := parent + "-backup"
	if err := os.Rename(parent, backup); err != nil {
		t.Fatalf("move parent: %v", err)
	}
	if err := os.Symlink(backup, parent); err != nil {
		t.Fatalf("replace parent with symlink: %v", err)
	}
	if err := reservation.validate(); err == nil {
		t.Fatal("validated reservation after parent replacement")
	}
	_ = os.Remove(parent)
	if err := os.Rename(backup, parent); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	if err := reservation.release(); err != nil {
		t.Fatalf("release restored reservation: %v", err)
	}
}

func TestManagedRootReservationReleaseRejectsReplacedParent(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	reservation, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("reserve root: %v", err)
	}
	parent := filepath.Dir(reservation.root)
	backup := parent + "-release-backup"
	if err := os.Rename(parent, backup); err != nil {
		t.Fatalf("move parent: %v", err)
	}
	if err := os.Symlink(backup, parent); err != nil {
		t.Fatalf("replace parent with symlink: %v", err)
	}
	if err := reservation.release(); err == nil {
		t.Fatal("released reservation through replaced parent")
	}
	if _, err := os.Lstat(filepath.Join(backup, filepath.Base(reservation.root))); err != nil {
		t.Fatalf("captured leaf was removed outside the base: %v", err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatalf("remove replacement symlink: %v", err)
	}
	if err := os.Rename(backup, parent); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	if err := os.Remove(reservation.root); err != nil {
		t.Fatalf("cleanup captured leaf: %v", err)
	}
}

func TestManagedRootReservationRejectsLeafReplacementBeforeGit(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	reservation, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("reserve root: %v", err)
	}
	original := reservation.root + "-original"
	if err := os.Rename(reservation.root, original); err != nil {
		t.Fatalf("move leaf: %v", err)
	}
	if err := os.Symlink(original, reservation.root); err != nil {
		t.Fatalf("replace leaf with symlink: %v", err)
	}
	if err := reservation.validate(); err == nil {
		t.Fatal("validated reservation after leaf replacement")
	}
	if err := os.Remove(reservation.root); err != nil {
		t.Fatalf("remove leaf symlink: %v", err)
	}
	if err := os.Rename(original, reservation.root); err != nil {
		t.Fatalf("restore leaf: %v", err)
	}
	if err := reservation.release(); err != nil {
		t.Fatalf("release restored leaf: %v", err)
	}
}

func TestManagedRootAllocatorExplicitRootSecurityContract(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	allocator := newManagedRootAllocator(nil, base, bytes.NewReader(nil))
	baseCanonical := allocator.base.path
	home := t.TempDir()
	t.Setenv("HOME", home)
	absolute := filepath.Join(t.TempDir(), "outside")
	tests := []struct {
		name    string
		request string
		want    string
		wantErr bool
	}{
		{name: "tilde", request: "~/requested", want: filepath.Join(home, "requested")},
		{name: "absolute outside base", request: absolute, want: absolute},
		{name: "relative under base", request: "nested/requested", want: filepath.Join(baseCanonical, "nested/requested")},
		{name: "dot", request: ".", wantErr: true},
		{name: "dot dot", request: "..", wantErr: true},
		{name: "escape", request: "../escape", wantErr: true},
		{name: "nested escape", request: "nested/../../escape", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := allocator.resolveExplicitRoot(tt.request)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveExplicitRoot(%q) succeeded", tt.request)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveExplicitRoot(%q): %v", tt.request, err)
			}
			want, err := config.CanonicalWorkspaceRoot(tt.want)
			if err != nil {
				t.Fatalf("canonical expected root: %v", err)
			}
			if got != want {
				t.Fatalf("resolveExplicitRoot(%q) = %q, want %q", tt.request, got, want)
			}
		})
	}
}

func TestManagedRootAllocatorAbsoluteExplicitBypassesInvalidBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "invalid-base")
	if err := os.WriteFile(base, nil, 0o600); err != nil {
		t.Fatalf("write invalid base: %v", err)
	}
	allocator := newManagedRootAllocator(nil, base, bytes.NewReader(nil))
	if _, err := allocator.resolveExplicitRoot(filepath.Join(t.TempDir(), "absolute")); err != nil {
		t.Fatalf("absolute explicit path used invalid base error: %v", err)
	}
	if _, err := allocator.resolveExplicitRoot("relative"); err == nil {
		t.Fatal("relative explicit path bypassed invalid base")
	}
}

func TestManagedRootAllocatorReleaseRefusesSymlinkReplacement(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	reservation, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("reserve root: %v", err)
	}
	target := t.TempDir()
	if err := os.Remove(reservation.root); err != nil {
		t.Fatalf("remove reservation: %v", err)
	}
	if err := os.Symlink(target, reservation.root); err != nil {
		t.Fatalf("replace reservation with symlink: %v", err)
	}
	if err := reservation.release(); err == nil {
		t.Fatal("released symlink replacement")
	}
	if info, err := os.Lstat(reservation.root); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replacement changed: info=%v err=%v", info, err)
	}
}

func TestManagedRootAllocatorReleaseRefusesEmptyReplacementIdentity(t *testing.T) {
	env := newServiceTestEnv(t)
	allocator := newManagedRootAllocator(env.store, env.baseDir, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	reservation, err := allocator.reserveRegularRoot(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("reserve root: %v", err)
	}
	original := reservation.root + "-original"
	if err := os.Rename(reservation.root, original); err != nil {
		t.Fatalf("move reservation: %v", err)
	}
	if err := os.Mkdir(reservation.root, 0o755); err != nil {
		t.Fatalf("replace reservation: %v", err)
	}
	if err := reservation.release(); err == nil {
		t.Fatal("released empty replacement")
	}
	if _, err := os.Lstat(reservation.root); err != nil {
		t.Fatalf("replacement missing: %v", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
