package gitref

import "fmt"

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

func (b RemoteBranch) RemoteName() string {
	return b.reference.nameComponents[0]
}

func (b RemoteBranch) BranchName() string {
	return joinComponents(b.reference.nameComponents[1:])
}
