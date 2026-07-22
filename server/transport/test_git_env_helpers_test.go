package transport

import "strings"

func sanitizeTestGitEnv(base []string) []string {
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		switch key {
		case "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_GLOB_PATHSPECS", "GIT_GRAFT_FILE", "GIT_ICASE_PATHSPECS", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_INTERNAL_SUPER_PREFIX", "GIT_LITERAL_PATHSPECS", "GIT_NAMESPACE", "GIT_NOGLOB_PATHSPECS", "GIT_NO_REPLACE_OBJECTS", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX", "GIT_REPLACE_REF_BASE", "GIT_SHALLOW_FILE", "GIT_WORK_TREE":
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") || strings.HasPrefix(key, "GIT_AUTHOR_") || strings.HasPrefix(key, "GIT_COMMITTER_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func appendTestGitCommitIdentityEnv(env []string) []string {
	return append(env,
		"GIT_AUTHOR_NAME=kent-test",
		"GIT_AUTHOR_EMAIL=kent-test@example.invalid",
		"GIT_COMMITTER_NAME=kent-test",
		"GIT_COMMITTER_EMAIL=kent-test@example.invalid",
	)
}
