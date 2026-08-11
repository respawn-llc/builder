package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

var (
	temporaryFileRootsOnce sync.Once
	temporaryFileRoots     []string
)

func IsPathInTemporaryDir(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	abs := path
	if !filepath.IsAbs(abs) {
		resolvedAbs, err := filepath.Abs(abs)
		if err != nil {
			return false
		}
		abs = resolvedAbs
	}
	abs = filepath.Clean(abs)
	for _, root := range tempFileRoots() {
		if pathWithinTemporaryRoot(abs, root) {
			return true
		}
	}
	return false
}

func tempFileRoots() []string {
	temporaryFileRootsOnce.Do(func() {
		seen := map[string]struct{}{}
		roots := make([]string, 0, 12)
		add := func(raw string) {
			for _, root := range existingTemporaryPathAliases(raw) {
				if _, ok := seen[root]; ok {
					continue
				}
				seen[root] = struct{}{}
				roots = append(roots, root)
			}
		}

		add(os.TempDir())
		add(os.Getenv("TMPDIR"))
		add(os.Getenv("TEMP"))
		add(os.Getenv("TMP"))
		if runtime.GOOS != "windows" {
			add("/tmp")
			add("/var/tmp")
			add("/private/tmp")
			add("/private/var/tmp")
		}

		sort.Strings(roots)
		temporaryFileRoots = roots
	})
	out := make([]string, len(temporaryFileRoots))
	copy(out, temporaryFileRoots)
	return out
}

func existingTemporaryPathAliases(path string) []string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	abs := trimmed
	if !filepath.IsAbs(abs) {
		resolvedAbs, err := filepath.Abs(abs)
		if err != nil {
			return nil
		}
		abs = resolvedAbs
	}
	cleaned := filepath.Clean(abs)
	info, err := os.Stat(cleaned)
	if err != nil || !info.IsDir() {
		return nil
	}
	aliases := []string{cleaned}
	if real, err := filepath.EvalSymlinks(cleaned); err == nil {
		real = filepath.Clean(real)
		if real != cleaned {
			aliases = append(aliases, real)
		}
	}
	return aliases
}

func pathWithinTemporaryRoot(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
