package patch

import (
	"os"

	"core/server/tools"
)

type OutsideWorkspaceErrorLabels = tools.FSGuardErrorLabels
type OutsideWorkspaceFailureFactory = tools.FSGuardFailureFactory
type OutsideWorkspaceGuard = tools.FSGuard

func NewOutsideWorkspaceGuard(workspaceRoot string, workspaceRootReal string, workspaceRootInfo os.FileInfo, workspaceOnly bool, allowOutsideWorkspace bool, approver OutsideWorkspaceApprover, sessionAllowed func() bool, setSessionAllowed func(bool), rejectionInstruction string, errorLabels OutsideWorkspaceErrorLabels, failures OutsideWorkspaceFailureFactory, temporaryPathAllowed func(string) bool, onApproved func(OutsideWorkspaceRequest, string)) OutsideWorkspaceGuard {
	return NewOutsideWorkspaceGuardWithScope(singleFileAccessScope(workspaceRoot, workspaceRootReal, workspaceRootInfo), workspaceOnly, allowOutsideWorkspace, approver, sessionAllowed, setSessionAllowed, rejectionInstruction, errorLabels, failures, temporaryPathAllowed, onApproved, tools.PathDenyPolicy{})
}

func NewOutsideWorkspaceGuardWithPolicy(workspaceRoot string, workspaceRootReal string, workspaceRootInfo os.FileInfo, workspaceOnly bool, allowOutsideWorkspace bool, approver OutsideWorkspaceApprover, sessionAllowed func() bool, setSessionAllowed func(bool), rejectionInstruction string, errorLabels OutsideWorkspaceErrorLabels, failures OutsideWorkspaceFailureFactory, temporaryPathAllowed func(string) bool, onApproved func(OutsideWorkspaceRequest, string), pathDenyPolicy tools.PathDenyPolicy) OutsideWorkspaceGuard {
	return NewOutsideWorkspaceGuardWithScope(singleFileAccessScope(workspaceRoot, workspaceRootReal, workspaceRootInfo), workspaceOnly, allowOutsideWorkspace, approver, sessionAllowed, setSessionAllowed, rejectionInstruction, errorLabels, failures, temporaryPathAllowed, onApproved, pathDenyPolicy)
}

func singleFileAccessScope(workspaceRoot string, workspaceRootReal string, workspaceRootInfo os.FileInfo) tools.FileAccessScope {
	root := tools.FilesystemRoot{LexicalPath: workspaceRoot, RealPath: workspaceRootReal, Info: workspaceRootInfo}
	return tools.FileAccessScope{WorkingDirectory: root, ExecutionTargetRoot: root}
}

func NewOutsideWorkspaceGuardWithScope(scope tools.FileAccessScope, workspaceOnly bool, allowOutsideWorkspace bool, approver OutsideWorkspaceApprover, sessionAllowed func() bool, setSessionAllowed func(bool), rejectionInstruction string, errorLabels OutsideWorkspaceErrorLabels, failures OutsideWorkspaceFailureFactory, temporaryPathAllowed func(string) bool, onApproved func(OutsideWorkspaceRequest, string), pathDenyPolicy tools.PathDenyPolicy) OutsideWorkspaceGuard {
	if failures.NoPermission == nil {
		failures.NoPermission = noPermissionFailure
	}
	if failures.DefaultApprovalFailed == nil {
		failures.DefaultApprovalFailed = approvalFailedFailure
	}
	if failures.DefaultUserDenied == nil {
		failures.DefaultUserDenied = userDeniedFailure
	}
	return tools.NewFSGuard(tools.FSGuardConfig{
		Scope:                 scope,
		WorkspaceOnly:         workspaceOnly,
		AllowOutsideWorkspace: allowOutsideWorkspace,
		Approver:              tools.FSGuardApprover(approver),
		SessionAllowed:        sessionAllowed,
		SetSessionAllowed:     setSessionAllowed,
		RejectionInstruction:  rejectionInstruction,
		ErrorLabels:           errorLabels,
		Failures:              failures,
		TemporaryPathAllowed:  temporaryPathAllowed,
		OnApproved: func(req tools.FSGuardRequest, reason string) {
			if onApproved != nil {
				onApproved(req, reason)
			}
		},
		PathDenyPolicy: pathDenyPolicy,
	})
}
