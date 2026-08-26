package workflowsvc

import (
	"core/shared/serverapi"
	"core/shared/worktreecontract"
)

func workflowRetainedPreviousWorktree(retained *worktreecontract.RetainedPreviousWorktree) *serverapi.RetainedPreviousWorktree {
	if retained == nil {
		return nil
	}
	return &serverapi.RetainedPreviousWorktree{Worktree: workflowTopologyEntry(retained.Worktree)}
}

func workflowTopologyEntry(entry worktreecontract.TopologyEntry) serverapi.WorktreeTopologyEntry {
	result := serverapi.WorktreeTopologyEntry{Variant: serverapi.WorktreeTopologyVariant(entry.Variant)}
	if entry.Registered != nil {
		result.Registered = &serverapi.WorktreeRegisteredFacts{
			Git:  workflowGitFacts(entry.Registered.Git),
			Kent: workflowKentFacts(entry.Registered.Kent),
		}
	}
	if entry.External != nil {
		result.External = &serverapi.WorktreeExternalFacts{Git: workflowGitFacts(entry.External.Git)}
	}
	if entry.Missing != nil {
		result.Missing = &serverapi.WorktreeMissingFacts{Kent: workflowKentFacts(entry.Missing.Kent)}
	}
	return result
}

func workflowGitFacts(facts worktreecontract.GitFacts) serverapi.WorktreeGitFacts {
	return serverapi.WorktreeGitFacts{
		CanonicalRoot:  facts.CanonicalRoot,
		HeadObject:     facts.HeadObject,
		BranchRef:      facts.BranchRef,
		BranchName:     facts.BranchName,
		Detached:       facts.Detached,
		Bare:           facts.Bare,
		LockedReason:   facts.LockedReason,
		PrunableReason: facts.PrunableReason,
		IsMain:         facts.IsMain,
		PathAvailable:  facts.PathAvailable,
	}
}

func workflowKentFacts(facts worktreecontract.KentFacts) serverapi.WorktreeKentFacts {
	return serverapi.WorktreeKentFacts{
		WorktreeID:      facts.WorktreeID,
		CanonicalRoot:   facts.CanonicalRoot,
		DisplayName:     facts.DisplayName,
		Managed:         facts.Managed,
		CreatedBranch:   facts.CreatedBranch,
		OriginSessionID: facts.OriginSessionID,
	}
}
