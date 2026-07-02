package runtimewire

import (
	"context"
	"encoding/json"

	"core/prompts"
	"core/server/tools"
	askquestion "core/server/tools"
	triggerhandofftool "core/server/tools"
	edittool "core/server/tools/edit"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	shelltool "core/server/tools/shell"
	"core/shared/config"
	"core/shared/toolspec"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Logger interface {
	Logf(format string, args ...any)
}

// errWorkspaceRootRequired is returned when a local tool registry binding is created or rebound without a workspace root.
var errWorkspaceRootRequired = errors.New("workspace root is required")

type LocalToolRuntimeContext struct {
	WorkspaceRoot                   string
	OwnerSessionID                  string
	ShellOutputMaxChars             int
	AllowNonCwdEdits                bool
	SupportsVision                  bool
	AskQuestionBroker               *askquestion.AskQuestionBroker
	QuestionsEnabledGetter          func() bool
	BackgroundShellManager          *shelltool.Manager
	TriggerHandoffController        func() triggerhandofftool.TriggerHandoffController
	OutsideWorkspaceEditApprover    patchtool.OutsideWorkspaceApprover
	OutsideWorkspaceReadApprover    patchtool.OutsideWorkspaceApprover
	ViewImageOutsideWorkspaceLogger readimagetool.OutsideWorkspaceAuditLogger
	EditPathDenyPolicy              tools.PathDenyPolicy
}

type LocalToolRegistryBinding struct {
	registry *tools.Registry
	ctx      LocalToolRuntimeContext
	enabled  []toolspec.ID
}

func BuildLocalRuntimeHandler(def tools.Definition, ctx LocalToolRuntimeContext) (tools.Handler, error) {
	switch def.LocalRuntimeBuilder() {
	case tools.LocalRuntimeBuilderExecCommand:
		if ctx.BackgroundShellManager == nil {
			return nil, fmt.Errorf("exec_command background manager is unavailable")
		}
		return shelltool.NewExecCommandTool(ctx.WorkspaceRoot, ctx.ShellOutputMaxChars, ctx.BackgroundShellManager, ctx.OwnerSessionID), nil
	case tools.LocalRuntimeBuilderWriteStdin:
		if ctx.BackgroundShellManager == nil {
			return nil, fmt.Errorf("write_stdin background manager is unavailable")
		}
		return shelltool.NewWriteStdinTool(ctx.ShellOutputMaxChars, ctx.BackgroundShellManager), nil
	case tools.LocalRuntimeBuilderPatch:
		if ctx.OutsideWorkspaceEditApprover == nil {
			return nil, fmt.Errorf("patch outside-workspace approver is unavailable")
		}
		return patchtool.New(
			ctx.WorkspaceRoot,
			true,
			patchtool.WithAllowOutsideWorkspace(ctx.AllowNonCwdEdits),
			patchtool.WithOutsideWorkspaceApprover(ctx.OutsideWorkspaceEditApprover),
			patchtool.WithPathDenyPolicy(ctx.EditPathDenyPolicy),
		)
	case tools.LocalRuntimeBuilderEdit:
		if ctx.OutsideWorkspaceEditApprover == nil {
			return nil, fmt.Errorf("edit outside-workspace approver is unavailable")
		}
		return edittool.New(
			ctx.WorkspaceRoot,
			true,
			edittool.WithAllowOutsideWorkspace(ctx.AllowNonCwdEdits),
			edittool.WithOutsideWorkspaceApprover(ctx.OutsideWorkspaceEditApprover),
			edittool.WithPathDenyPolicy(ctx.EditPathDenyPolicy),
		)
	case tools.LocalRuntimeBuilderAskQuestion:
		if ctx.AskQuestionBroker == nil {
			return nil, fmt.Errorf("ask_question broker is unavailable")
		}
		return askquestion.NewAskQuestionTool(ctx.AskQuestionBroker, ctx.QuestionsEnabledGetter), nil
	case tools.LocalRuntimeBuilderCompleteNode:
		return completeNodeUnavailableTool{}, nil
	case tools.LocalRuntimeBuilderTriggerHandoff:
		if ctx.TriggerHandoffController == nil {
			return nil, fmt.Errorf("trigger_handoff controller is unavailable")
		}
		return triggerhandofftool.NewTriggerHandoffTool(ctx.TriggerHandoffController), nil
	case tools.LocalRuntimeBuilderViewImage:
		if ctx.OutsideWorkspaceReadApprover == nil {
			return nil, fmt.Errorf("view_image outside-workspace approver is unavailable")
		}
		opts := []readimagetool.Option{
			readimagetool.WithAllowOutsideWorkspace(ctx.AllowNonCwdEdits),
			readimagetool.WithOutsideWorkspaceApprover(ctx.OutsideWorkspaceReadApprover),
		}
		if ctx.ViewImageOutsideWorkspaceLogger != nil {
			opts = append(opts, readimagetool.WithOutsideWorkspaceAuditLogger(ctx.ViewImageOutsideWorkspaceLogger))
		}
		return readimagetool.New(ctx.WorkspaceRoot, ctx.SupportsVision, opts...)
	default:
		return nil, fmt.Errorf("unsupported local runtime builder %q for tool %q", def.LocalRuntimeBuilder(), def.ID)
	}
}

type completeNodeUnavailableTool struct{}

func (completeNodeUnavailableTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	output, err := json.Marshal(map[string]string{"error": "complete_node is only available during a workflow run"})
	if err != nil {
		output = json.RawMessage(`{"error":"complete_node is only available during a workflow run"}`)
	}
	return tools.Result{CallID: c.ID, Name: toolspec.ToolCompleteNode, IsError: true, Output: output, Summary: "not in workflow run"}, nil
}

func (b *LocalToolRegistryBinding) Registry() *tools.Registry {
	if b == nil {
		return nil
	}
	return b.registry
}

func (b *LocalToolRegistryBinding) Rebind(workspaceRoot string) error {
	if b == nil {
		return fmt.Errorf("local tool registry binding is required")
	}
	trimmedRoot := strings.TrimSpace(workspaceRoot)
	if trimmedRoot == "" {
		return errWorkspaceRootRequired
	}
	b.ctx.WorkspaceRoot = trimmedRoot
	return b.rebuild()
}

func (b *LocalToolRegistryBinding) rebuild() error {
	if b == nil {
		return fmt.Errorf("local tool registry binding is required")
	}
	if b.registry == nil {
		b.registry = tools.NewRegistry()
	}
	handlers := make([]tools.HandlerRegistration, 0, len(b.enabled))
	enabledSet := make(map[toolspec.ID]struct{}, len(b.enabled))
	for _, id := range b.enabled {
		enabledSet[id] = struct{}{}
	}
	for _, id := range tools.CatalogIDs() {
		if _, ok := enabledSet[id]; !ok {
			continue
		}
		def, ok := tools.DefinitionFor(id)
		if !ok {
			return fmt.Errorf("missing tool definition for %q", id)
		}
		if !def.AvailableInLocalRuntime() {
			continue
		}
		handler, err := BuildLocalRuntimeHandler(def, b.ctx)
		if err != nil {
			return wrapSessionWorkspaceRetargetHint(b.ctx.OwnerSessionID, b.ctx.WorkspaceRoot, err)
		}
		handlers = append(handlers, tools.HandlerRegistration{ID: id, Handler: handler})
	}
	b.registry.ReplaceHandlers(handlers...)
	return nil
}

type LocalToolRegistryOptions struct {
	WorkspaceRoot            string
	OwnerSessionID           string
	Enabled                  []toolspec.ID
	MinimumExecToBgTime      time.Duration
	ShellOutputMaxChars      int
	AllowNonCwdEdits         bool
	SupportsVision           bool
	Logger                   Logger
	Background               *shelltool.Manager
	TriggerHandoffController func() triggerhandofftool.TriggerHandoffController
	QuestionsEnabledGetter   func() bool
	GlobalConfigDir          string
}

func NewLocalToolRegistryBinding(opts LocalToolRegistryOptions) (*LocalToolRegistryBinding, *askquestion.AskQuestionBroker, *shelltool.Manager, error) {
	trimmedRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if trimmedRoot == "" {
		return nil, nil, nil, errWorkspaceRootRequired
	}
	broker := askquestion.NewAskQuestionBroker()
	background := opts.Background
	if background == nil {
		var err error
		background, err = shelltool.NewManager(shelltool.WithMinimumExecToBgTime(opts.MinimumExecToBgTime))
		if err != nil {
			return nil, nil, nil, err
		}
	}
	background.SetMinimumExecToBgTime(opts.MinimumExecToBgTime)
	patchOutsideWorkspaceApprover := NewOutsideWorkspaceApprover(broker, "editing")
	readOutsideWorkspaceApprover := NewOutsideWorkspaceApprover(broker, "reading")
	var editPathDenyPolicy tools.PathDenyPolicy
	if enabledToolsNeedEditDenyPolicy(opts.Enabled) {
		var err error
		editPathDenyPolicy, err = generatedAssetsEditDenyPolicy(opts.GlobalConfigDir)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	registry := tools.NewRegistry()
	ctx := LocalToolRuntimeContext{
		WorkspaceRoot:                trimmedRoot,
		OwnerSessionID:               opts.OwnerSessionID,
		ShellOutputMaxChars:          opts.ShellOutputMaxChars,
		AllowNonCwdEdits:             opts.AllowNonCwdEdits,
		SupportsVision:               opts.SupportsVision,
		AskQuestionBroker:            broker,
		QuestionsEnabledGetter:       opts.QuestionsEnabledGetter,
		BackgroundShellManager:       background,
		TriggerHandoffController:     opts.TriggerHandoffController,
		OutsideWorkspaceEditApprover: patchtool.OutsideWorkspaceApprover(patchOutsideWorkspaceApprover.Approve),
		OutsideWorkspaceReadApprover: patchtool.OutsideWorkspaceApprover(readOutsideWorkspaceApprover.Approve),
		EditPathDenyPolicy:           editPathDenyPolicy,
		ViewImageOutsideWorkspaceLogger: readimagetool.OutsideWorkspaceAuditLogger(func(entry readimagetool.OutsideWorkspaceAudit) {
			if opts.Logger == nil {
				return
			}
			opts.Logger.Logf(
				"tool.view_image.outside_workspace.approved requested=%q resolved=%q reason=%s",
				entry.RequestedPath,
				entry.ResolvedPath,
				entry.Reason,
			)
		}),
	}
	binding := &LocalToolRegistryBinding{
		registry: registry,
		ctx:      ctx,
		enabled:  append([]toolspec.ID(nil), opts.Enabled...),
	}
	if err := binding.rebuild(); err != nil {
		return nil, nil, nil, err
	}
	return binding, broker, background, nil
}

func BuildToolRegistry(opts LocalToolRegistryOptions) (*tools.Registry, *askquestion.AskQuestionBroker, *shelltool.Manager, error) {
	binding, broker, background, err := NewLocalToolRegistryBinding(opts)
	if err != nil {
		return nil, nil, nil, err
	}
	return binding.Registry(), broker, background, nil
}

func enabledToolsNeedEditDenyPolicy(enabled []toolspec.ID) bool {
	for _, id := range enabled {
		if id == toolspec.ToolPatch || id == toolspec.ToolEdit {
			return true
		}
	}
	return false
}

func generatedAssetsEditDenyPolicy(configRoot string) (tools.PathDenyPolicy, error) {
	layout, err := prompts.GeneratedLayoutFor(configRoot)
	if err != nil {
		return tools.PathDenyPolicy{}, err
	}
	label := "kent generated assets"
	return tools.CompilePathDenyPolicy([]tools.PathDenyRuleConfig{{
		Label:   &label,
		Message: generatedAssetsEditDenyMessage(configRoot, layout.UserSkillsRoot),
		Matcher: tools.PathMatcherConfig{
			Kind:        tools.PathMatcherLiteral,
			Pattern:     layout.GeneratedRoot,
			LiteralTree: true,
		},
	}})
}

func generatedAssetsEditDenyMessage(configRoot string, userSkillsRoot string) string {
	skillsPath := filepath.Clean(userSkillsRoot) + string(filepath.Separator)
	if isDefault, err := config.IsDefaultPersistenceRoot(configRoot); err == nil && isDefault {
		skillsPath = config.PersistenceRoot + "/skills/"
	}
	return "Do NOT attempt to edit Kent's generated files; they are overwritten every session. You cannot edit generated skills. Consider instead copying them as " + skillsPath + " and exactly matching name/id/directory structure so that the new file automatically shadows the generated skill by Kent runtime."
}

func wrapSessionWorkspaceRetargetHint(sessionID string, workspaceRoot string, err error) error {
	if strings.TrimSpace(sessionID) == "" || err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return err
	}
	newWorkspaceRoot := "."
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		newWorkspaceRoot = filepath.Clean(cwd)
	}
	return sessionWorkspaceRetargetError{
		sessionID:     strings.TrimSpace(sessionID),
		workspaceRoot: trimmedWorkspaceRoot,
		newRoot:       newWorkspaceRoot,
		cause:         err,
	}
}

type sessionWorkspaceRetargetError struct {
	sessionID     string
	workspaceRoot string
	newRoot       string
	cause         error
}

func (e sessionWorkspaceRetargetError) Error() string {
	return fmt.Sprintf(
		"workspace root %q is missing; run `kent rebind %s %s`",
		e.workspaceRoot,
		strconv.Quote(e.sessionID),
		strconv.Quote(e.newRoot),
	)
}

func (e sessionWorkspaceRetargetError) Unwrap() error {
	return e.cause
}
