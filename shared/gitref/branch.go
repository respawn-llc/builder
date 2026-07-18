package gitref

import (
	"errors"
	"fmt"
	"strings"
)

type LocalBranch struct {
	reference Reference
}

func ParseLocalBranch(raw string) (LocalBranch, error) {
	reference, err := Parse(raw)
	if err != nil {
		return LocalBranch{}, err
	}
	if reference.Namespace() != NamespaceLocalBranch {
		return LocalBranch{}, fmt.Errorf("Git ref %q is not a local branch", raw)
	}
	return LocalBranch{reference: reference}, nil
}

func (b LocalBranch) Ref() string {
	return b.reference.String()
}

func (b LocalBranch) Name() string {
	return b.reference.Name()
}

func ParseOptionalLocalBranch(ref *string, name *string) (*LocalBranch, error) {
	if ref == nil && name == nil {
		return nil, nil
	}
	if ref == nil || name == nil {
		return nil, errors.New("local branch ref and name must be provided together")
	}
	branch, err := ParseLocalBranch(*ref)
	if err != nil {
		return nil, fmt.Errorf("local branch ref is invalid: %w", err)
	}
	if *name != branch.Name() {
		return nil, fmt.Errorf("local branch name %q does not match ref %q", *name, *ref)
	}
	return &branch, nil
}

type RemoteBranch struct {
	reference Reference
}

func ParseRemoteBranch(raw string) (RemoteBranch, error) {
	reference, err := Parse(raw)
	if err != nil {
		return RemoteBranch{}, err
	}
	if reference.Namespace() != NamespaceRemote {
		return RemoteBranch{}, fmt.Errorf("Git ref %q is not a remote branch", raw)
	}
	if len(reference.nameComponents) < 2 {
		return RemoteBranch{}, fmt.Errorf("Git remote ref %q has no branch name", raw)
	}
	return RemoteBranch{reference: reference}, nil
}

func (b RemoteBranch) Ref() string {
	return b.reference.String()
}

func (b RemoteBranch) BranchNameForRemote(remoteName string) (string, error) {
	if remoteName == "" || remoteName != strings.TrimSpace(remoteName) {
		return "", errors.New("Git remote name must be nonblank and have no surrounding whitespace")
	}
	remoteComponents := strings.Split(remoteName, "/")
	for _, component := range remoteComponents {
		if component == "" {
			return "", fmt.Errorf("Git remote name %q contains an empty component", remoteName)
		}
	}
	if len(b.reference.nameComponents) <= len(remoteComponents) {
		return "", fmt.Errorf("Git remote ref %q has no branch for remote %q", b.Ref(), remoteName)
	}
	for index, component := range remoteComponents {
		if b.reference.nameComponents[index] != component {
			return "", fmt.Errorf("Git remote ref %q resolves outside remote %q", b.Ref(), remoteName)
		}
	}
	return joinComponents(b.reference.nameComponents[len(remoteComponents):]), nil
}
