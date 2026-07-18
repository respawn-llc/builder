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

func TestParseOptionalLocalBranchOwnsTheCompleteRefNamePairInvariant(t *testing.T) {
	const branchRef = "refs/heads/feature/nested"
	const branchName = "feature/nested"
	branch, err := ParseOptionalLocalBranch(stringPointer(branchRef), stringPointer(branchName))
	if err != nil {
		t.Fatalf("ParseOptionalLocalBranch: %v", err)
	}
	if branch == nil || branch.Ref() != branchRef || branch.Name() != branchName {
		t.Fatalf("optional local branch = %+v", branch)
	}
	absent, err := ParseOptionalLocalBranch(nil, nil)
	if err != nil || absent != nil {
		t.Fatalf("absent optional local branch = %+v, %v", absent, err)
	}
	for _, pair := range []struct {
		ref  *string
		name *string
	}{
		{ref: stringPointer(branchRef)},
		{name: stringPointer(branchName)},
		{ref: stringPointer(branchRef), name: stringPointer("feature/other")},
		{ref: stringPointer("refs/tags/v1"), name: stringPointer("v1")},
	} {
		if _, err := ParseOptionalLocalBranch(pair.ref, pair.name); err == nil {
			t.Fatalf("ParseOptionalLocalBranch(%v, %v) succeeded", pair.ref, pair.name)
		}
	}
}

func TestParseRemoteBranchResolvesBranchNameAgainstKnownRemote(t *testing.T) {
	branch, err := ParseRemoteBranch("refs/remotes/team/origin/feature/nested")
	if err != nil {
		t.Fatalf("ParseRemoteBranch: %v", err)
	}
	branchName, err := branch.BranchNameForRemote("team/origin")
	if err != nil {
		t.Fatalf("BranchNameForRemote: %v", err)
	}
	if branch.Ref() != "refs/remotes/team/origin/feature/nested" || branchName != "feature/nested" {
		t.Fatalf("remote branch = %q/%q", branch.Ref(), branchName)
	}
	for _, remoteName := range []string{"", " team/origin", "team/origin ", "team//origin", "other"} {
		if _, err := branch.BranchNameForRemote(remoteName); err == nil {
			t.Fatalf("BranchNameForRemote(%q) succeeded", remoteName)
		}
	}
}

func stringPointer(value string) *string {
	return &value
}
