package app

import (
	"time"

	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/tools"
	askquestion "core/server/tools"
	triggerhandofftool "core/server/tools"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	shelltool "core/server/tools/shell"
	"core/shared/toolspec"
)

type backgroundEventRouter struct {
	inner       *runtimewire.BackgroundEventRouter
	background  *shelltool.Manager
	outputLimit int
	outputMode  shelltool.BackgroundOutputMode
}

func newBackgroundEventRouter(background *shelltool.Manager, outputLimit int, outputMode shelltool.BackgroundOutputMode) *backgroundEventRouter {
	return &backgroundEventRouter{
		inner:       runtimewire.NewBackgroundEventRouter(background, outputLimit, outputMode),
		background:  background,
		outputLimit: outputLimit,
		outputMode:  outputMode,
	}
}

func (r *backgroundEventRouter) ensureInner() *runtimewire.BackgroundEventRouter {
	if r == nil {
		return nil
	}
	if r.inner == nil {
		r.inner = runtimewire.NewBackgroundEventRouter(r.background, r.outputLimit, r.outputMode)
	}
	return r.inner
}

func (r *backgroundEventRouter) SetActiveSession(sessionID string, engine *runtime.Engine) {
	if inner := r.ensureInner(); inner != nil {
		inner.SetActiveSession(sessionID, engine)
	}
}

func (r *backgroundEventRouter) ClearActiveSession(sessionID string, engine *runtime.Engine) {
	if inner := r.ensureInner(); inner != nil {
		inner.ClearActiveSession(sessionID, engine)
	}
}

func (r *backgroundEventRouter) handle(evt shelltool.Event) {
	if inner := r.ensureInner(); inner != nil {
		inner.Handle(evt)
	}
}

type localToolRuntimeContext struct {
	workspaceRoot                   string
	ownerSessionID                  string
	shellOutputMaxChars             int
	allowNonCwdEdits                bool
	supportsVision                  bool
	askQuestionBroker               *askquestion.AskQuestionBroker
	backgroundShellManager          *shelltool.Manager
	triggerHandoffController        func() triggerhandofftool.TriggerHandoffController
	outsideWorkspaceEditApprover    patchtool.OutsideWorkspaceApprover
	outsideWorkspaceReadApprover    patchtool.OutsideWorkspaceApprover
	viewImageOutsideWorkspaceLogger readimagetool.OutsideWorkspaceAuditLogger
}

func buildLocalRuntimeHandler(def tools.Definition, ctx localToolRuntimeContext) (tools.Handler, error) {
	return runtimewire.BuildLocalRuntimeHandler(def, runtimewire.LocalToolRuntimeContext{
		WorkspaceRoot:                   ctx.workspaceRoot,
		OwnerSessionID:                  ctx.ownerSessionID,
		ShellOutputMaxChars:             ctx.shellOutputMaxChars,
		AllowNonCwdEdits:                ctx.allowNonCwdEdits,
		SupportsVision:                  ctx.supportsVision,
		AskQuestionBroker:               ctx.askQuestionBroker,
		BackgroundShellManager:          ctx.backgroundShellManager,
		TriggerHandoffController:        ctx.triggerHandoffController,
		OutsideWorkspaceEditApprover:    ctx.outsideWorkspaceEditApprover,
		OutsideWorkspaceReadApprover:    ctx.outsideWorkspaceReadApprover,
		ViewImageOutsideWorkspaceLogger: ctx.viewImageOutsideWorkspaceLogger,
	})
}

func buildToolRegistry(workspaceRoot string, ownerSessionID string, enabled []toolspec.ID, minimumExecToBgTime time.Duration, shellOutputMaxChars int, allowNonCwdEdits bool, supportsVision bool, logger *runLogger, background *shelltool.Manager) (*tools.Registry, *askquestion.AskQuestionBroker, *shelltool.Manager, error) {
	return runtimewire.BuildToolRegistry(runtimewire.LocalToolRegistryOptions{
		WorkspaceRoot:       workspaceRoot,
		OwnerSessionID:      ownerSessionID,
		Enabled:             enabled,
		MinimumExecToBgTime: minimumExecToBgTime,
		ShellOutputMaxChars: shellOutputMaxChars,
		AllowNonCwdEdits:    allowNonCwdEdits,
		SupportsVision:      supportsVision,
		Logger:              logger,
		Background:          background,
	})
}
