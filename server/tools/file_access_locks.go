package tools

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var pathLocks sync.Map

func lockFileAccessPath(path string) func() {
	key := canonicalLockKey(path)
	if key == "" {
		return func() {}
	}
	value, _ := pathLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func LockFileAccessPaths(paths []string) func() {
	if len(paths) == 0 {
		return func() {}
	}
	seen := map[string]struct{}{}
	ordered := make([]string, 0, len(paths))
	for _, path := range paths {
		key := canonicalLockKey(path)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	unlocks := make([]func(), 0, len(ordered))
	for _, path := range ordered {
		unlocks = append(unlocks, lockFileAccessPath(path))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

func canonicalLockKey(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if abs, err := filepath.Abs(trimmed); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(trimmed)
}
