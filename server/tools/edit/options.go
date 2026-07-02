package edit

import "core/server/tools"

type Option func(*Tool)

func WithAllowOutsideWorkspace(allow bool) Option {
	return func(t *Tool) {
		t.allowOutsideWorkspace = allow
	}
}

func WithOutsideWorkspaceApprover(approver tools.FSGuardApprover) Option {
	return func(t *Tool) {
		t.outsideWorkspaceApprover = approver
	}
}

func WithPathDenyPolicy(policy tools.PathDenyPolicy) Option {
	return func(t *Tool) {
		t.pathDenyPolicy = policy
	}
}
