package runtimewire

import (
	"context"
	"encoding/json"

	"core/prompts"
	"core/server/metadata"
	"core/server/tools"
	askquestion "core/server/tools"
	triggerhandofftool "core/server/tools"
	edittool "core/server/tools/edit"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Logger interface {
	Logf(format string, args ...any)
}

// errWorkspaceRootRequired is returned when a local tool registry binding is created or rebound without a workspace root.
var errWorkspaceRootRequired = errors.New("workspace root is required")

type LocalToolRuntimeContext struct {
	FilesystemContext               tools.FilesystemContext
	OwnerSessionID                  string
	ExecutionCorrelation            *runtimeids.ExecutionCorrelation
	ShellOutputMaxChars             int
	AllowNonCwdEdits                bool
	SupportsVision                  bool
	AskQuestionBroker               *askquestion.AskQuestionBroker
	QuestionsEnabledGetter          func() bool
	BackgroundShellManager          *shelltool.Manager
	ShellPostprocessor              *postprocess.Runner
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
	mu       sync.Mutex
}

func BuildLocalRuntimeHandler(def tools.Definition, ctx LocalToolRuntimeContext) (tools.Handler, error) {
	switch def.LocalRuntimeBuilder() {
	case tools.LocalRuntimeBuilderExecCommand:
		workingDirectory := ctx.FilesystemContext.Access.WorkingDirectory.LexicalPath
		if ctx.BackgroundShellManager == nil {
			return nil, fmt.Errorf("exec_command background manager is unavailable")
		}
		return shelltool.NewExecCommandToolWithConfig(workingDirectory, ctx.ShellOutputMaxChars, ctx.BackgroundShellManager, ctx.OwnerSessionID, shelltool.ExecCommandToolConfig{
			Postprocessor:        ctx.ShellPostprocessor,
			ExecutionCorrelation: ctx.ExecutionCorrelation,
		}), nil
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
			ctx.FilesystemContext.Access.WorkingDirectory.LexicalPath,
			true,
			patchtool.WithAllowOutsideWorkspace(ctx.AllowNonCwdEdits),
			patchtool.WithOutsideWorkspaceApprover(ctx.OutsideWorkspaceEditApprover),
			patchtool.WithPathDenyPolicy(ctx.EditPathDenyPolicy),
			patchtool.WithFileAccessScope(ctx.FilesystemContext.Access),
			patchtool.WithManagedWorktreePathContext(ctx.FilesystemContext.ManagedWorktree),
		)
	case tools.LocalRuntimeBuilderEdit:
		if ctx.OutsideWorkspaceEditApprover == nil {
			return nil, fmt.Errorf("edit outside-workspace approver is unavailable")
		}
		return edittool.New(
			ctx.FilesystemContext.Access.WorkingDirectory.LexicalPath,
			true,
			edittool.WithAllowOutsideWorkspace(ctx.AllowNonCwdEdits),
			edittool.WithOutsideWorkspaceApprover(ctx.OutsideWorkspaceEditApprover),
			edittool.WithPathDenyPolicy(ctx.EditPathDenyPolicy),
			edittool.WithFileAccessScope(ctx.FilesystemContext.Access),
			edittool.WithManagedWorktreePathContext(ctx.FilesystemContext.ManagedWorktree),
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
			readimagetool.WithFileAccessScope(ctx.FilesystemContext.Access),
		}
		if ctx.ViewImageOutsideWorkspaceLogger != nil {
			opts = append(opts, readimagetool.WithOutsideWorkspaceAuditLogger(ctx.ViewImageOutsideWorkspaceLogger))
		}
		return readimagetool.New(ctx.FilesystemContext.Access.WorkingDirectory.LexicalPath, ctx.SupportsVision, opts...)
	default:
		return nil, fmt.Errorf("unsupported local runtime builder %q for tool %q", def.LocalRuntimeBuilder(), def.ID)
	}
}

type completeNodeUnavailableTool struct{}

func (completeNodeUnavailableTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	output, err := json.Marshal(map[string]string{"error": "complete_node is only available during Current Node execution"})
	if err != nil {
		output = json.RawMessage(`{"error":"complete_node is only available during Current Node execution"}`)
	}
	return tools.Result{CallID: c.ID, Name: toolspec.ToolCompleteNode, IsError: true, Output: output, Summary: textutil.Value("not in Current Node execution")}, nil
}

func (b *LocalToolRegistryBinding) Registry() *tools.Registry {
	if b == nil {
		return nil
	}
	return b.registry
}

func (b *LocalToolRegistryBinding) ReplaceFilesystemContext(next tools.FilesystemContext) error {
	if b == nil {
		return fmt.Errorf("local tool registry binding is required")
	}
	if err := validateFilesystemContext(next); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := b.ctx.FilesystemContext
	b.ctx.FilesystemContext = next.Clone()
	if err := b.rebuildLocked(); err != nil {
		b.ctx.FilesystemContext = previous
		return err
	}
	return nil
}

func (b *LocalToolRegistryBinding) FilesystemContext() tools.FilesystemContext {
	if b == nil {
		return tools.FilesystemContext{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctx.FilesystemContext.Clone()
}

// BindExecutionCorrelation binds the exact execution scope for subsequently constructed local tool handlers.
// Passing nil returns the registered resource to its unscoped idle state.
func (b *LocalToolRegistryBinding) BindExecutionCorrelation(correlation *runtimeids.ExecutionCorrelation) error {
	if b == nil {
		return fmt.Errorf("local tool registry binding is required")
	}
	if correlation != nil {
		if err := correlation.Validate(); err != nil {
			return fmt.Errorf("validate execution correlation: %w", err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := b.ctx.ExecutionCorrelation
	b.ctx.ExecutionCorrelation = correlation
	if err := b.rebuildLocked(); err != nil {
		b.ctx.ExecutionCorrelation = previous
		return err
	}
	return nil
}

func (b *LocalToolRegistryBinding) rebuild() error {
	if b == nil {
		return fmt.Errorf("local tool registry binding is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rebuildLocked()
}

func (b *LocalToolRegistryBinding) rebuildLocked() error {
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
			return err
		}
		handlers = append(handlers, tools.HandlerRegistration{ID: id, Handler: handler})
	}
	b.registry.ReplaceHandlers(handlers...)
	return nil
}

type LocalToolRegistryOptions struct {
	FilesystemContext        tools.FilesystemContext
	OwnerSessionID           string
	ExecutionCorrelation     *runtimeids.ExecutionCorrelation
	Enabled                  []toolspec.ID
	MinimumExecToBgTime      time.Duration
	ShellOutputMaxChars      int
	AllowNonCwdEdits         bool
	SupportsVision           bool
	Logger                   Logger
	Background               *shelltool.Manager
	ShellPostprocessor       *postprocess.Runner
	TriggerHandoffController func() triggerhandofftool.TriggerHandoffController
	QuestionsEnabledGetter   func() bool
	GlobalConfigDir          string
}

func NewLocalToolRegistryBinding(opts LocalToolRegistryOptions) (*LocalToolRegistryBinding, *askquestion.AskQuestionBroker, *shelltool.Manager, error) {
	if err := validateFilesystemContext(opts.FilesystemContext); err != nil {
		return nil, nil, nil, err
	}
	if opts.ExecutionCorrelation != nil {
		if err := opts.ExecutionCorrelation.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf("validate execution correlation: %w", err)
		}
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
		FilesystemContext:            opts.FilesystemContext.Clone(),
		OwnerSessionID:               opts.OwnerSessionID,
		ExecutionCorrelation:         opts.ExecutionCorrelation,
		ShellOutputMaxChars:          opts.ShellOutputMaxChars,
		AllowNonCwdEdits:             opts.AllowNonCwdEdits,
		SupportsVision:               opts.SupportsVision,
		AskQuestionBroker:            broker,
		QuestionsEnabledGetter:       opts.QuestionsEnabledGetter,
		BackgroundShellManager:       background,
		ShellPostprocessor:           opts.ShellPostprocessor,
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

func NewFilesystemContext(workdir string, targetRoot string, boundary metadata.ProjectWorkspaceBoundary) (tools.FilesystemContext, error) {
	normalizedBoundary, err := boundary.Normalize()
	if err != nil {
		return tools.FilesystemContext{}, err
	}
	working, target, err := requiredFilesystemRoots(workdir, targetRoot)
	if err != nil {
		return tools.FilesystemContext{}, err
	}
	if !filesystemRootContains(target, working.RealPath) {
		return tools.FilesystemContext{}, fmt.Errorf("working directory %q is outside execution target root %q", workdir, targetRoot)
	}
	scope := tools.ProjectWorkspaceScope{ProjectID: normalizedBoundary.ProjectID, Roots: make([]tools.ProjectWorkspaceRoot, 0, len(normalizedBoundary.Workspaces))}
	for _, workspace := range normalizedBoundary.Workspaces {
		root := strings.TrimSpace(workspace.CanonicalRoot)
		resolvedFilesystemRoot, err := secondaryRootForPath(workspace)
		if err != nil {
			return tools.FilesystemContext{}, fmt.Errorf("resolve project workspace %q: %w", root, err)
		}
		scope.Roots = append(scope.Roots, tools.ProjectWorkspaceRoot{
			WorkspaceID:    workspace.WorkspaceID,
			FilesystemRoot: resolvedFilesystemRoot,
		})
	}
	return tools.FilesystemContext{Access: tools.FileAccessScope{
		WorkingDirectory: working, ExecutionTargetRoot: target, ProjectWorkspace: scope,
	}}, nil
}

func NewManagedWorktreePathContext(boundary metadata.ProjectWorkspaceBoundary, currentWorktreeRoot *string) (*tools.ManagedWorktreePathContext, error) {
	normalizedBoundary, err := boundary.Normalize()
	if err != nil {
		return nil, err
	}
	if len(normalizedBoundary.ManagedWorktrees) == 0 {
		return nil, nil
	}
	roots := make([]string, 0, len(normalizedBoundary.ManagedWorktrees))
	for _, worktree := range normalizedBoundary.ManagedWorktrees {
		roots = append(roots, worktree.CanonicalRoot)
	}
	return tools.NewManagedWorktreePathContextForRoots(roots, currentWorktreeRoot)
}

func WithExecutionTarget(current tools.FilesystemContext, workdir string, targetRoot string, managed *tools.ManagedWorktreePathContext) (tools.FilesystemContext, error) {
	working, target, err := requiredFilesystemRoots(workdir, targetRoot)
	if err != nil {
		return tools.FilesystemContext{}, err
	}
	next := current.Clone()
	next.Access.WorkingDirectory = working
	next.Access.ExecutionTargetRoot = target
	if !filesystemRootContains(target, working.RealPath) {
		return tools.FilesystemContext{}, fmt.Errorf("working directory %q is outside execution target root %q", workdir, targetRoot)
	}
	next.ManagedWorktree = managed
	return next, nil
}

func requiredFilesystemRoots(workdir, targetRoot string) (tools.FilesystemRoot, tools.FilesystemRoot, error) {
	working, err := requiredRootForPath(workdir)
	if err != nil {
		return tools.FilesystemRoot{}, tools.FilesystemRoot{}, fmt.Errorf("resolve working directory: %w", err)
	}
	target, err := requiredRootForPath(targetRoot)
	if err != nil {
		return tools.FilesystemRoot{}, tools.FilesystemRoot{}, fmt.Errorf("resolve execution target root: %w", err)
	}
	return working, target, nil
}

func requiredRootForPath(root string) (tools.FilesystemRoot, error) {
	resolved, err := trustedRootForPath(root)
	if err != nil {
		return tools.FilesystemRoot{}, err
	}
	if resolved.Info == nil {
		return tools.FilesystemRoot{}, fmt.Errorf("%w: required filesystem root %q is unavailable", os.ErrNotExist, root)
	}
	return resolved, nil
}

func validateFilesystemContext(context tools.FilesystemContext) error {
	if strings.TrimSpace(context.Access.WorkingDirectory.LexicalPath) == "" ||
		strings.TrimSpace(context.Access.ExecutionTargetRoot.LexicalPath) == "" {
		return errWorkspaceRootRequired
	}
	if strings.TrimSpace(context.Access.ProjectWorkspace.ProjectID) == "" {
		return errors.New("project workspace project id is required")
	}
	if len(context.Access.ProjectWorkspace.Roots) > metadata.ProjectWorkspaceCollectionLimit {
		return fmt.Errorf(
			"project workspace boundary contains %d roots, maximum is %d",
			len(context.Access.ProjectWorkspace.Roots),
			metadata.ProjectWorkspaceCollectionLimit,
		)
	}
	if strings.TrimSpace(context.Access.WorkingDirectory.RealPath) == "" ||
		strings.TrimSpace(context.Access.ExecutionTargetRoot.RealPath) == "" {
		return errWorkspaceRootRequired
	}
	if context.Access.WorkingDirectory.Info == nil || context.Access.ExecutionTargetRoot.Info == nil {
		return fmt.Errorf("%w: required filesystem root is unavailable", os.ErrNotExist)
	}
	if !filesystemRootContains(context.Access.ExecutionTargetRoot, context.Access.WorkingDirectory.RealPath) {
		return errors.New("working directory is outside execution target root")
	}
	for _, workspace := range context.Access.ProjectWorkspace.Roots {
		if strings.TrimSpace(workspace.LexicalPath) == "" || strings.TrimSpace(workspace.RealPath) == "" {
			return errors.New("project workspace filesystem root is invalid")
		}
	}
	return nil
}

func secondaryRootForPath(workspace metadata.ProjectWorkspace) (tools.FilesystemRoot, error) {
	root := strings.TrimSpace(workspace.CanonicalRoot)
	if root == "" {
		return tools.FilesystemRoot{}, errors.New("filesystem root is required")
	}
	resolved, err := trustedRootForPath(root)
	if err != nil {
		return tools.FilesystemRoot{}, err
	}
	return resolved, nil
}

func filesystemRootContains(root tools.FilesystemRoot, candidate string) bool {
	relative, err := filepath.Rel(root.RealPath, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func trustedRootForPath(root string) (tools.FilesystemRoot, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return tools.FilesystemRoot{}, errors.New("filesystem root is required")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return tools.FilesystemRoot{}, fmt.Errorf("resolve trusted root: %w", err)
	}
	info, statErr := os.Stat(absolute)
	if errors.Is(statErr, os.ErrNotExist) {
		real, err := config.ResolveExistingAncestorRealPath(absolute)
		if err != nil {
			return tools.FilesystemRoot{}, fmt.Errorf("resolve trusted root real path: %w", err)
		}
		return tools.FilesystemRoot{LexicalPath: absolute, RealPath: real}, nil
	}
	if statErr != nil {
		return tools.FilesystemRoot{}, fmt.Errorf("stat trusted root: %w", statErr)
	}
	real, err := config.ResolveExistingAncestorRealPath(absolute)
	if err != nil {
		return tools.FilesystemRoot{}, fmt.Errorf("resolve trusted root real path: %w", err)
	}
	return tools.FilesystemRoot{LexicalPath: absolute, RealPath: real, Info: info}, nil
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
