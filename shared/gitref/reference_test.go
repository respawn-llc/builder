package gitref

import "testing"

func TestParseReturnsTypedNamespaceAndName(t *testing.T) {
	tests := []struct {
		raw       string
		namespace Namespace
		name      string
	}{
		{raw: "refs/heads/feature/nested", namespace: NamespaceLocalBranch, name: "feature/nested"},
		{raw: "refs/tags/v1.2.3", namespace: NamespaceTag, name: "v1.2.3"},
		{raw: "refs/remotes/origin/main", namespace: NamespaceRemote, name: "origin/main"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			ref, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			if ref.String() != tt.raw || ref.Namespace() != tt.namespace || ref.Name() != tt.name {
				t.Fatalf("reference = %q/%v/%q", ref.String(), ref.Namespace(), ref.Name())
			}
		})
	}
}

func TestParseLocalBranchRejectsOtherNamespacesAndInvalidRefs(t *testing.T) {
	for _, raw := range []string{
		"",
		" refs/heads/main",
		"refs/heads/main ",
		"main",
		"refs/heads/",
		"refs/heads//main",
		"refs/heads/.hidden",
		"refs/heads/feature.",
		"refs/heads/feature.lock",
		"refs/heads/feature..next",
		"refs/heads/feature@{next",
		"refs/heads/feature name",
		"refs/heads/feature~next",
		"refs/heads/feature^next",
		"refs/heads/feature:next",
		"refs/heads/feature?next",
		"refs/heads/feature*next",
		"refs/heads/feature[next",
		`refs/heads/feature\next`,
		"refs/tags/v1",
		"refs/remotes/origin/main",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseLocalBranch(raw); err == nil {
				t.Fatalf("ParseLocalBranch(%q) succeeded", raw)
			}
		})
	}
}

func TestParseLocalBranchOwnsCanonicalRefAndDerivedName(t *testing.T) {
	branch, err := ParseLocalBranch("refs/heads/feature/nested")
	if err != nil {
		t.Fatalf("ParseLocalBranch: %v", err)
	}
	if branch.Ref() != "refs/heads/feature/nested" || branch.Name() != "feature/nested" {
		t.Fatalf("local branch = %q/%q", branch.Ref(), branch.Name())
	}
}

func TestParseRemoteBranchReturnsTypedRemoteAndBranchNames(t *testing.T) {
	branch, err := ParseRemoteBranch("refs/remotes/origin/feature/nested")
	if err != nil {
		t.Fatalf("ParseRemoteBranch: %v", err)
	}
	if branch.Ref() != "refs/remotes/origin/feature/nested" ||
		branch.RemoteName() != "origin" ||
		branch.BranchName() != "feature/nested" {
		t.Fatalf("remote branch = %q/%q/%q", branch.Ref(), branch.RemoteName(), branch.BranchName())
	}
}
