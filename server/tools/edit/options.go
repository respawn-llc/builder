package edit

import "core/server/tools"

type options struct {
	allowOutsideWorkspace    bool
	outsideWorkspaceApprover tools.FileAccessApprover
	pathDenyPolicy           tools.PathDenyPolicy
}

type Option func(*options)

func WithAllowOutsideWorkspace(allow bool) Option {
	return func(options *options) {
		options.allowOutsideWorkspace = allow
	}
}

func WithOutsideWorkspaceApprover(approver tools.FileAccessApprover) Option {
	return func(options *options) {
		options.outsideWorkspaceApprover = approver
	}
}

func WithPathDenyPolicy(policy tools.PathDenyPolicy) Option {
	return func(options *options) {
		options.pathDenyPolicy = policy
	}
}
