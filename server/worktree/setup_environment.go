package worktree

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	setupEnvironmentKeySourceWorkspaceRoot = "KENT_WORKTREE_SOURCE_WORKSPACE_ROOT"
	setupEnvironmentKeyBranchName          = "KENT_WORKTREE_BRANCH_NAME"
	setupEnvironmentKeyWorktreeRoot        = "KENT_WORKTREE_ROOT"
	setupEnvironmentKeySessionID           = "KENT_WORKTREE_SESSION_ID"
	setupEnvironmentKeyProjectID           = "KENT_WORKTREE_PROJECT_ID"
	setupEnvironmentKeyWorkspaceID         = "KENT_WORKTREE_WORKSPACE_ID"
	setupEnvironmentKeyWorktreeID          = "KENT_WORKTREE_WORKTREE_ID"
	setupEnvironmentKeyCreatedBranch       = "KENT_WORKTREE_CREATED_BRANCH"
	setupEnvironmentKeyPayloadJSON         = "KENT_WORKTREE_PAYLOAD_JSON"
)

var reservedSetupEnvironmentKeys = []string{
	setupEnvironmentKeySourceWorkspaceRoot,
	setupEnvironmentKeyBranchName,
	setupEnvironmentKeyWorktreeRoot,
	setupEnvironmentKeySessionID,
	setupEnvironmentKeyProjectID,
	setupEnvironmentKeyWorkspaceID,
	setupEnvironmentKeyWorktreeID,
	setupEnvironmentKeyCreatedBranch,
	setupEnvironmentKeyPayloadJSON,
}

type setupEnvironmentKeyCanonicalizer func(string) string

func caseSensitiveSetupEnvironmentKey(key string) string {
	return key
}

func caseInsensitiveSetupEnvironmentKey(key string) string {
	return strings.ToUpper(key)
}

func buildSetupEnvironment(inherited []string, payload setupScriptPayload, canonicalize setupEnvironmentKeyCanonicalizer) ([]string, error) {
	if canonicalize == nil {
		return nil, fmt.Errorf("setup environment key canonicalizer is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reserved := make(map[string]struct{}, len(reservedSetupEnvironmentKeys))
	for _, key := range reservedSetupEnvironmentKeys {
		reserved[canonicalize(key)] = struct{}{}
	}
	env := make([]string, 0, len(inherited)+len(reservedSetupEnvironmentKeys))
	for _, entry := range inherited {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			env = append(env, entry)
			continue
		}
		if _, isReserved := reserved[canonicalize(key)]; !isReserved {
			env = append(env, entry)
		}
	}
	env = append(env,
		setupEnvironmentKeySourceWorkspaceRoot+"="+payload.SourceWorkspaceRoot,
		setupEnvironmentKeyBranchName+"="+payload.BranchName,
		setupEnvironmentKeyWorktreeRoot+"="+payload.WorktreeRoot,
		setupEnvironmentKeyProjectID+"="+payload.ProjectID,
		setupEnvironmentKeyWorkspaceID+"="+payload.WorkspaceID,
		setupEnvironmentKeyWorktreeID+"="+payload.WorktreeID,
		fmt.Sprintf("%s=%t", setupEnvironmentKeyCreatedBranch, payload.CreatedBranch),
		setupEnvironmentKeyPayloadJSON+"="+string(body),
	)
	if payload.SessionID != nil {
		env = append(env, setupEnvironmentKeySessionID+"="+*payload.SessionID)
	}
	return env, nil
}
