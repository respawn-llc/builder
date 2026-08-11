package runtimewirefixture

import (
	"os"
	"path/filepath"
	"testing"

	"core/server/tools"
)

func FilesystemContext(t testing.TB, root string) tools.FilesystemContext {
	t.Helper()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve filesystem root %q: %v", root, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat filesystem root %q: %v", root, err)
	}
	filesystemRoot := tools.FilesystemRoot{LexicalPath: root, RealPath: real, Info: info}
	return tools.FilesystemContext{Access: tools.FileAccessScope{
		WorkingDirectory:    filesystemRoot,
		ExecutionTargetRoot: filesystemRoot,
		ProjectWorkspace:    tools.ProjectWorkspaceScope{ProjectID: "test"},
	}}
}
