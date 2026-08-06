package worktree

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/shared/config"
)

func TestNormalizeWorkspacePathKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "portable folder", in: "Builder CLI", want: "builder-cli"},
		{name: "collapse separators", in: "A___B...C", want: "a-b-c"},
		{name: "non ASCII", in: "Café 東京", want: "caf"},
		{name: "reserved name", in: "CON", want: "workspace"},
		{name: "reserved numbered name", in: "COM1", want: "workspace"},
		{name: "fallback", in: "___", want: "workspace"},
		{name: "bounded", in: "abcdefghijklmnopqrstuvwxYZ", want: "abcdefghijklmnopqrstuvwx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWorkspacePathKey(tt.in); got != tt.want {
				t.Fatalf("normalizeWorkspacePathKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestManagedRootAllocatorUsesSharedNormalizedWorkspaceParent(t *testing.T) {
	base := filepath.Join(t.TempDir(), "worktrees")
	rootA := filepath.Join(t.TempDir(), "Builder CLI")
	rootB := filepath.Join(t.TempDir(), "Builder CLI")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatalf("create workspace A: %v", err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatalf("create workspace B: %v", err)
	}
	allocator := newManagedRootAllocator(base, strings.NewReader("entropy"))
	parentA, err := allocator.ensureWorkspaceParent(rootA)
	if err != nil {
		t.Fatalf("ensure workspace parent A: %v", err)
	}
	parentB, err := allocator.ensureWorkspaceParent(rootB)
	if err != nil {
		t.Fatalf("ensure workspace parent B: %v", err)
	}
	if parentA != parentB {
		t.Fatalf("same-named workspaces used different parents: %q and %q", parentA, parentB)
	}
	if filepath.Base(parentA) != "builder-cli" {
		t.Fatalf("workspace parent = %q, want builder-cli", parentA)
	}
}

func TestManagedRootAllocatorRejectsAutomaticParentOverlappingSourceWorkspace(t *testing.T) {
	tests := []struct {
		name      string
		base      func(root string) string
		workspace func(root string) string
	}{
		{
			name:      "parent equals source workspace",
			base:      func(root string) string { return root },
			workspace: func(root string) string { return filepath.Join(root, "app") },
		},
		{
			name:      "parent is inside source workspace",
			base:      func(root string) string { return filepath.Join(root, "app", "worktrees") },
			workspace: func(root string) string { return filepath.Join(root, "app") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := tt.workspace(root)
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			allocator := newManagedRootAllocator(tt.base(root), strings.NewReader("entropy"))
			if _, err := allocator.reserveRegularRoot(workspace); !errors.Is(err, errManagedRootOverlapsSourceWorkspace) {
				t.Fatalf("reserve overlapping automatic root error = %v, want overlap error", err)
			}
		})
	}
}

func TestManagedRootAllocatorAllowsParentContainingSourceWorkspace(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "app", "app")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	allocator := newManagedRootAllocator(base, bytes.NewReader(bytes.Repeat([]byte{4}, 16)))
	regularRoot, err := allocator.reserveRegularRoot(workspace)
	if err != nil {
		t.Fatalf("reserve regular root: %v", err)
	}
	taskRoot, err := allocator.reserveTaskRoot(workspace, "KENT-335")
	if err != nil {
		t.Fatalf("reserve Task root: %v", err)
	}
	wantParent := filepath.Join(allocator.base.path, "app")
	for _, root := range []string{regularRoot, taskRoot} {
		if filepath.Dir(root) != wantParent {
			t.Fatalf("allocated root = %q, want parent %q", root, wantParent)
		}
		if sameOrDescendantPath(workspace, root) {
			t.Fatalf("allocated root %q is inside source workspace %q", root, workspace)
		}
	}
}

func TestManagedRootAllocatorReservesRegularAndTaskLeaves(t *testing.T) {
	base := filepath.Join(t.TempDir(), "worktrees")
	workspace := filepath.Join(t.TempDir(), "Builder CLI")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	allocator := newManagedRootAllocator(base, bytes.NewReader(bytes.Repeat([]byte{4}, 32)))
	regular, err := allocator.reserveRegularRoot(workspace)
	if err != nil {
		t.Fatalf("reserve regular root: %v", err)
	}
	if filepath.Base(regular) != "444" {
		t.Fatalf("regular root = %q, want 444", regular)
	}
	task, err := allocator.reserveTaskRoot(workspace, "KENT-335")
	if err != nil {
		t.Fatalf("reserve Task root: %v", err)
	}
	if filepath.Base(task) != "KENT-335" {
		t.Fatalf("Task root = %q, want KENT-335", task)
	}
}

func TestRemoveEmptyManagedRootAfterAddFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reserved")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create reserved root: %v", err)
	}
	if err := removeEmptyManagedRootAfterAddFailure(root); err != nil {
		t.Fatalf("remove empty reserved root: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved root still exists: %v", err)
	}
	if err := removeEmptyManagedRootAfterAddFailure(root); err != nil {
		t.Fatalf("repeat absent cleanup: %v", err)
	}

	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("recreate reserved root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "partial"), nil, 0o600); err != nil {
		t.Fatalf("write partial Git state: %v", err)
	}
	if err := removeEmptyManagedRootAfterAddFailure(root); err == nil {
		t.Fatal("removed non-empty reserved root")
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("non-empty reserved root was removed: %v", err)
	}
}

func TestManagedRootAllocatorRetriesDirectLeafCollisions(t *testing.T) {
	base := filepath.Join(t.TempDir(), "worktrees")
	workspace := filepath.Join(t.TempDir(), "Builder CLI")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	allocator := newManagedRootAllocator(base, bytes.NewReader([]byte{
		1, 1, 1,
		2, 2, 2, 2,
	}))
	parent, err := allocator.ensureWorkspaceParent(workspace)
	if err != nil {
		t.Fatalf("ensure workspace parent: %v", err)
	}
	for _, leaf := range []string{"111", "KENT-335"} {
		if err := os.Mkdir(filepath.Join(parent, leaf), 0o755); err != nil {
			t.Fatalf("occupy %q: %v", leaf, err)
		}
	}
	regular, err := allocator.reserveRegularRoot(workspace)
	if err != nil {
		t.Fatalf("reserve regular root: %v", err)
	}
	if filepath.Base(regular) != "2222" {
		t.Fatalf("regular root = %q, want 2222", regular)
	}

	taskAllocator := newManagedRootAllocator(base, bytes.NewReader(bytes.Repeat([]byte{4}, 8)))
	task, err := taskAllocator.reserveTaskRoot(workspace, "KENT-335")
	if err != nil {
		t.Fatalf("reserve colliding Task root: %v", err)
	}
	if filepath.Base(task) != "KENT-335-444" {
		t.Fatalf("Task root = %q, want KENT-335-444", task)
	}
}

func TestManagedRootAllocatorPanicsAfterCollisionAttemptsAreExhausted(t *testing.T) {
	base := filepath.Join(t.TempDir(), "worktrees")
	workspace := filepath.Join(t.TempDir(), "Builder CLI")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	allocator := newManagedRootAllocator(base, bytes.NewReader(bytes.Repeat([]byte{1}, 18)))
	parent, err := allocator.ensureWorkspaceParent(workspace)
	if err != nil {
		t.Fatalf("ensure workspace parent: %v", err)
	}
	for _, leaf := range []string{"KENT-335", "KENT-335-111", "KENT-335-1111", "KENT-335-11111", "KENT-335-111111"} {
		if err := os.Mkdir(filepath.Join(parent, leaf), 0o755); err != nil {
			t.Fatalf("occupy %q: %v", leaf, err)
		}
	}
	defer func() {
		value := recover()
		exhaustion, ok := value.(*managedRootExhaustionError)
		if !ok || exhaustion.TaskShortID == nil || *exhaustion.TaskShortID != "KENT-335" {
			t.Fatalf("panic = %#v, want typed Task exhaustion", value)
		}
	}()
	_, _ = allocator.reserveTaskRoot(workspace, "KENT-335")
}

func TestManagedRootAllocatorExplicitRootContract(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	allocator := newManagedRootAllocator(base, bytes.NewReader(nil))
	home := t.TempDir()
	t.Setenv("HOME", home)
	absolute := filepath.Join(t.TempDir(), "outside")
	tests := []struct {
		name    string
		request string
		want    string
		wantErr bool
	}{
		{name: "tilde outside base", request: "~/requested", wantErr: true},
		{name: "absolute outside base", request: absolute, wantErr: true},
		{name: "absolute under base", request: filepath.Join(base, "explicit"), want: filepath.Join(base, "explicit")},
		{name: "relative under base", request: "nested/requested", want: filepath.Join(allocator.base.path, "nested/requested")},
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

func TestManagedRootAllocatorRejectsInvalidBaseAndParent(t *testing.T) {
	baseFile := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(baseFile, nil, 0o600); err != nil {
		t.Fatalf("write invalid base: %v", err)
	}
	invalidBase := newManagedRootAllocator(baseFile, strings.NewReader("entropy"))
	if !errors.Is(invalidBase.base.err, errManagedRootBaseInvalid) {
		t.Fatalf("base error = %v, want invalid base", invalidBase.base.err)
	}

	base := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "source"), nil, 0o600); err != nil {
		t.Fatalf("write incompatible parent: %v", err)
	}
	allocator := newManagedRootAllocator(base, strings.NewReader("entropy"))
	if _, err := allocator.ensureWorkspaceParent(workspace); err == nil {
		t.Fatal("accepted incompatible workspace parent")
	}
}

func TestManagedRootAllocatorReturnsEntropyFailure(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	allocator := newManagedRootAllocator(t.TempDir(), errorReader{})
	if _, err := allocator.reserveRegularRoot(workspace); err == nil {
		t.Fatal("entropy failure was swallowed")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
