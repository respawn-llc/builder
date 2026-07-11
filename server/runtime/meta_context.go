package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/toolspec"
)

type metaContextKind uint8

const (
	metaContextKindUnknown metaContextKind = iota
	metaContextKindAgents
	metaContextKindSkills
	metaContextKindSubagents
	metaContextKindEnvironment
	metaContextKindHeadless
	metaContextKindHeadlessExit
	metaContextKindWorkflow
	metaContextKindWorktree
	metaContextKindWorktreeExit
)

type metaContextClassification struct {
	kind        metaContextKind
	key         string
	sourcePath  string
	messageType llm.MessageType
}

type metaContextBuildOptions struct {
	ExistingMessages          []llm.Message
	IncludeAgents             bool
	IncludeSkills             bool
	IncludeSubagents          bool
	SubagentInvocationContext config.SubagentInvocationContext
	IncludeEnvironment        bool
	IncludeHeadless           bool
	IncludeHeadlessExit       bool
	IncludeWorkflow           bool
	WorkflowCompletionMode    workflowruntime.CompletionMode
	WorkflowRun               *workflowruntime.Config
	WorkflowTaskCommentCount  int64
	IncludeSkillWarnings      bool
	PermissiveAgentsReadError bool
}

type metaContextBuildResult struct {
	Agents        []llm.Message
	SkillWarnings []string
	Skills        []llm.Message
	Subagents     []llm.Message
	Environment   []llm.Message
	Headless      []llm.Message
	HeadlessExit  []llm.Message
	Workflow      []llm.Message
	Worktree      []llm.Message
	WorktreeExit  []llm.Message
}

func (r metaContextBuildResult) OrderedMetaMessages() []llm.Message {
	out := make([]llm.Message, 0, len(r.Agents)+len(r.Skills)+len(r.Subagents)+len(r.Environment)+len(r.Headless)+len(r.HeadlessExit)+len(r.Workflow)+len(r.Worktree)+len(r.WorktreeExit))
	out = append(out, r.OrderedBaseMessages()...)
	out = append(out, r.Headless...)
	out = append(out, r.HeadlessExit...)
	out = append(out, r.Workflow...)
	out = append(out, r.Worktree...)
	out = append(out, r.WorktreeExit...)
	return out
}

func (r metaContextBuildResult) OrderedBaseMessages() []llm.Message {
	out := make([]llm.Message, 0, len(r.Agents)+len(r.Skills)+len(r.Subagents)+len(r.Environment))
	out = append(out, r.Environment...)
	out = append(out, r.Skills...)
	out = append(out, r.Subagents...)
	out = append(out, r.Agents...)
	return out
}

type metaContextBuilder struct {
	workspaceRoot    string
	environmentCWD   string
	globalConfigDir  string
	model            string
	thinkingLevel    string
	disabledSkills   map[string]bool
	subagentSettings config.Settings
	enabledTools     []toolspec.ID
	now              time.Time
}

func newMetaContextBuilder(workspaceRoot, model, thinkingLevel string, disabledSkills map[string]bool, now time.Time) metaContextBuilder {
	trimmedRoot := strings.TrimSpace(workspaceRoot)
	return metaContextBuilder{
		workspaceRoot:  trimmedRoot,
		environmentCWD: trimmedRoot,
		model:          strings.TrimSpace(model),
		thinkingLevel:  strings.TrimSpace(thinkingLevel),
		disabledSkills: normalizedDisabledSkills(disabledSkills),
		now:            now,
	}
}

func baseMetaContextBuildOptions(includeSkillWarnings bool) metaContextBuildOptions {
	return metaContextBuildOptions{
		IncludeAgents:             true,
		IncludeSkills:             true,
		IncludeSubagents:          true,
		SubagentInvocationContext: config.SubagentInvocationContextOrdinary,
		IncludeEnvironment:        true,
		IncludeSkillWarnings:      includeSkillWarnings,
	}
}

func (b metaContextBuilder) withEnvironmentCWD(cwd string) metaContextBuilder {
	if trimmed := strings.TrimSpace(cwd); trimmed != "" {
		b.environmentCWD = trimmed
	}
	return b
}

// withGlobalConfigDir selects the absolute persistence root that owns global
// model-visible context (global AGENTS.md, skills, generated skills). Empty
// leaves the home-based default in effect.
func (b metaContextBuilder) withGlobalConfigDir(globalConfigDir string) metaContextBuilder {
	b.globalConfigDir = strings.TrimSpace(globalConfigDir)
	return b
}

func (b metaContextBuilder) withSubagents(settings config.Settings, enabledTools []toolspec.ID) metaContextBuilder {
	b.subagentSettings = settings
	b.enabledTools = append([]toolspec.ID(nil), enabledTools...)
	return b
}

func (b metaContextBuilder) Build(opts metaContextBuildOptions) (metaContextBuildResult, error) {
	ranks, rankErr := b.agentPathRanks()
	if rankErr != nil && opts.IncludeAgents && !opts.PermissiveAgentsReadError {
		return metaContextBuildResult{}, rankErr
	}
	collector := newMetaContextCollector(ranks)
	collector.addMessages(opts.ExistingMessages)

	if opts.IncludeAgents {
		agents, err := b.discoverAgents(opts.PermissiveAgentsReadError)
		if err != nil {
			return metaContextBuildResult{}, err
		}
		collector.addMessages(agents)
	}

	if opts.IncludeSkills {
		skills, issues, err := discoverInjectedSkills(b.workspaceRoot, b.globalConfigDir, b.disabledSkills)
		if err != nil {
			return metaContextBuildResult{}, err
		}
		if opts.IncludeSkillWarnings {
			collector.addWarnings(skillDiscoveryWarningTexts(issues))
		}
		if len(skills) > 0 {
			collector.addMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: llm.MessageTypeSkills,
				Content:     renderSkillsContext(skills),
			}})
		}
	}

	if opts.IncludeSubagents {
		if message, ok := b.subagentsMetaMessage(opts.SubagentInvocationContext); ok {
			collector.addMessages([]llm.Message{message})
		}
	}

	if opts.IncludeEnvironment {
		environmentMessage, err := environmentContextMessage(b.environmentCWD, b.model, b.now)
		if err != nil {
			return metaContextBuildResult{}, err
		}
		collector.addMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeEnvironment,
			Content:     environmentMessage,
		}})
	}

	if opts.IncludeHeadless {
		if message, ok := headlessModeMetaMessage(); ok {
			collector.addMessages([]llm.Message{message})
		}
	}

	if opts.IncludeHeadlessExit {
		if message, ok := headlessModeExitMetaMessage(); ok {
			collector.addMessages([]llm.Message{message})
		}
	}
	if opts.IncludeWorkflow {
		message, ok, err := workflowModeMetaMessage(opts.WorkflowCompletionMode, opts.WorkflowRun, opts.WorkflowTaskCommentCount)
		if err != nil {
			return metaContextBuildResult{}, err
		}
		if ok {
			if opts.WorkflowRun != nil {
				message.SourcePath = strings.TrimSpace(string(opts.WorkflowRun.Contract.RunID))
			}
			collector.addMessages([]llm.Message{message})
		}
	}

	return collector.result(), nil
}

func (b metaContextBuilder) agentPathRanks() (map[string]int, error) {
	paths, err := agentsInjectionPaths(b.workspaceRoot, b.globalConfigDir)
	if err != nil {
		return nil, err
	}
	ranks := make(map[string]int, len(paths))
	for idx, path := range paths {
		ranks[agentSourceKey(path)] = idx
	}
	return ranks, nil
}

func (b metaContextBuilder) discoverAgents(permissive bool) ([]llm.Message, error) {
	paths, err := agentsInjectionPaths(b.workspaceRoot, b.globalConfigDir)
	if err != nil {
		if permissive {
			return nil, nil
		}
		return nil, err
	}
	out := make([]llm.Message, 0, len(paths))
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) || permissive {
				continue
			}
			return nil, fmt.Errorf("read AGENTS.md: %w", readErr)
		}
		out = append(out, llm.Message{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeAgentsMD,
			SourcePath:  path,
			Content:     fmt.Sprintf("%s\nsource: %s\n\n```%s\n%s\n```", agentsInjectedHeader, path, agentsInjectedFenceLabel, string(data)),
		})
	}
	return out, nil
}

func (b metaContextBuilder) subagentsMetaMessage(context config.SubagentInvocationContext) (llm.Message, bool) {
	if !toolEnabled(b.enabledTools, toolspec.ToolExecCommand) {
		return llm.Message{}, false
	}
	roles := b.renderableSubagentRoles(context)
	if len(roles) == 0 {
		return llm.Message{}, false
	}
	lines := make([]string, 0, len(roles)+3)
	lines = append(lines, "Available subagent roles:")
	lines = append(lines, "- `default`: not specifying any role will invoke the default general-purpose agent")
	for _, role := range roles {
		lines = append(lines, "- `"+role.Name+"`: "+role.Description)
	}
	lines = append(lines, "---")
	lines = append(lines, "Invoke with `"+prompts.LaunchCommand()+" run --agent=<role> \"<prompt>\"`.")
	return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeSubagents, Content: strings.Join(lines, "\n")}, true
}

type renderedSubagentRole struct {
	Name        string
	Description string
}

func (b metaContextBuilder) renderableSubagentRoles(context config.SubagentInvocationContext) []renderedSubagentRole {
	settings := b.subagentSettings
	if len(settings.Subagents) == 0 {
		return nil
	}
	names := make([]string, 0, len(settings.Subagents))
	for name := range settings.Subagents {
		normalized := config.NormalizeSubagentRole(name)
		if normalized == "" || normalized == config.BuiltInSubagentRoleFast {
			continue
		}
		names = append(names, normalized)
	}
	sort.Strings(names)
	out := make([]renderedSubagentRole, 0, len(names))
	for _, name := range names {
		role := settings.Subagents[name]
		if !config.SubagentRoleCallableInContext(settings, name, context) || !config.SubagentRoleHasMeaningfulDiff(settings, role) {
			continue
		}
		description := strings.TrimSpace(role.Description)
		if description == "" {
			description = fallbackSubagentDescription(settings, role)
		}
		if description == "" {
			continue
		}
		out = append(out, renderedSubagentRole{Name: name, Description: description})
	}
	return out
}

func fallbackSubagentDescription(base config.Settings, role config.SubagentRole) string {
	model := base.Model
	if _, ok := role.Sources["model"]; ok {
		model = role.Settings.Model
	}
	thinking := base.ThinkingLevel
	if _, ok := role.Sources["thinking_level"]; ok {
		thinking = role.Settings.ThinkingLevel
	}
	parts := []string{strings.TrimSpace(model), "thinking " + strings.TrimSpace(thinking)}
	if role.Sources["priority_request_mode"] == "file" && role.Settings.PriorityRequestMode {
		parts = append(parts, "fast mode on")
	}
	tools := effectiveRoleToolMap(base.EnabledTools, role)
	if tools[toolspec.ToolPatch] || tools[toolspec.ToolEdit] {
		parts = append(parts, "can edit")
	}
	if tools[toolspec.ToolExecCommand] {
		parts = append(parts, "can call shell")
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, ", ")
}

func effectiveRoleToolMap(base map[toolspec.ID]bool, role config.SubagentRole) map[toolspec.ID]bool {
	out := make(map[toolspec.ID]bool, len(base))
	for key, enabled := range base {
		out[key] = enabled
	}
	for _, id := range toolspec.CatalogIDs() {
		sourceKey := "tools." + toolspec.ConfigName(id)
		if _, ok := role.Sources[sourceKey]; ok {
			out[id] = role.Settings.EnabledTools[id]
		}
	}
	return out
}

func toolEnabled(enabled []toolspec.ID, want toolspec.ID) bool {
	for _, id := range enabled {
		if id == want {
			return true
		}
	}
	return false
}

func headlessModeMetaMessage() (llm.Message, bool) {
	content := strings.TrimSpace(prompts.HeadlessModePrompt)
	if content == "" {
		return llm.Message{}, false
	}
	return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: content}, true
}

func headlessModeExitMetaMessage() (llm.Message, bool) {
	content := strings.TrimSpace(prompts.HeadlessModeExitPrompt)
	if content == "" {
		return llm.Message{}, false
	}
	return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessModeExit, Content: content}, true
}

func workflowModeMetaMessage(mode workflowruntime.CompletionMode, cfg *workflowruntime.Config, taskCommentCount int64) (llm.Message, bool, error) {
	if cfg != nil {
		content, err := workflowTaskInstructionsContent(mode, cfg, taskCommentCount)
		if err != nil {
			return llm.Message{}, false, err
		}
		if strings.TrimSpace(content) == "" {
			return llm.Message{}, false, nil
		}
		return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorkflowMode, Content: content}, true, nil
	}
	content := ""
	var err error
	content, err = workflowCompletionInstructionsFragment(mode, "", workflowruntime.CompletionContract{})
	if err != nil {
		return llm.Message{}, false, err
	}
	if content == "" {
		return llm.Message{}, false, nil
	}
	return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorkflowMode, Content: content}, true, nil
}

func workflowTaskInstructionsContent(mode workflowruntime.CompletionMode, cfg *workflowruntime.Config, taskCommentCount int64) (string, error) {
	instructions := cfg.Instructions
	completionInstructions, err := workflowCompletionInstructionsFragment(mode, instructions.WorkflowShortID, cfg.Contract)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(completionInstructions) == "" {
		return "", nil
	}
	return prompts.RenderWorkflowTaskInstructions(prompts.WorkflowNodeContextArgs{
		TaskId:               instructions.TaskID,
		TaskShortId:          instructions.TaskShortID,
		TaskTitle:            instructions.TaskTitle,
		TaskBody:             instructions.TaskBody,
		WorkflowId:           instructions.WorkflowID,
		WorkflowShortId:      instructions.WorkflowShortID,
		NodeId:               instructions.NodeID,
		NodeKey:              instructions.NodeKey,
		NodeDisplayName:      instructions.NodeDisplayName,
		ContextMode:          instructions.ContextMode,
		SourceSessionID:      instructions.SourceSessionID,
		CompletionMode:       string(mode),
		TaskNumberOfComments: taskCommentCount,
		Transitions:          workflowInstructionTransitions(instructions.Transitions),
		NodePrompt:           instructions.NodePrompt,
	}, completionInstructions)
}

func workflowCompletionInstructionsFragment(mode workflowruntime.CompletionMode, workflowShortID string, contract workflowruntime.CompletionContract) (string, error) {
	switch mode {
	case workflowruntime.CompletionModeTool:
		return prompts.RenderWorkflowToolCompletionInstructions(workflowShortID)
	case workflowruntime.CompletionModeStructuredOutput:
		return prompts.RenderWorkflowStructuredCompletionInstructions(workflowShortID)
	case workflowruntime.CompletionModeShellCommand:
		return prompts.RenderWorkflowShellCompletionInstructions(workflowCompletionPromptArgs(workflowShortID, contract))
	case workflowruntime.CompletionModeUnstructuredOutput:
		return prompts.RenderWorkflowUnstructuredCompletionInstructions(workflowCompletionPromptArgs(workflowShortID, contract))
	default:
		return "", nil
	}
}

func workflowCompletionPromptArgs(workflowShortID string, contract workflowruntime.CompletionContract) prompts.WorkflowCompletionInstructionsArgs {
	return prompts.WorkflowCompletionInstructionsArgs{
		WorkflowShortID: strings.TrimSpace(workflowShortID),
		Contract: prompts.WorkflowCompletionContract{
			Transitions: workflowCompletionPromptTransitions(contract.Transitions),
		},
	}
}

func workflowCompletionPromptTransitions(in []workflowruntime.CompletionTransition) []prompts.WorkflowCompletionTransition {
	out := make([]prompts.WorkflowCompletionTransition, 0, len(in))
	for _, transition := range in {
		id := strings.TrimSpace(transition.ID)
		if id == "" {
			continue
		}
		out = append(out, prompts.WorkflowCompletionTransition{
			ID:          id,
			DisplayName: strings.TrimSpace(transition.DisplayName),
			Description: strings.TrimSpace(transition.Description),
			Parameters:  workflowCompletionPromptParameters(transition.Parameters),
		})
	}
	return out
}

func workflowCompletionPromptParameters(in []workflow.Parameter) []prompts.WorkflowCompletionParameter {
	out := make([]prompts.WorkflowCompletionParameter, 0, len(in))
	for _, parameter := range in {
		key := strings.TrimSpace(parameter.Key)
		if key == "" {
			continue
		}
		out = append(out, prompts.WorkflowCompletionParameter{Key: key, Description: strings.TrimSpace(parameter.Description)})
	}
	return out
}

func workflowInstructionTransitions(in []workflowruntime.TransitionInstruction) []prompts.WorkflowTransition {
	out := make([]prompts.WorkflowTransition, 0, len(in))
	for _, transition := range in {
		id := strings.TrimSpace(transition.ID)
		if id == "" {
			continue
		}
		out = append(out, prompts.WorkflowTransition{ID: id, DisplayName: strings.TrimSpace(transition.DisplayName), Description: strings.TrimSpace(transition.Description)})
	}
	return out
}

func worktreeModeMetaMessage(state session.WorktreeReminderState) (llm.Message, bool) {
	content := prompts.RenderWorktreeModePrompt(state.Branch, state.EffectiveCwd, state.WorktreePath, state.WorkspaceRoot)
	if strings.TrimSpace(content) == "" {
		return llm.Message{}, false
	}
	return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorktreeMode, Content: content, CompactContent: worktreeReminderCondensedText(state), SourcePath: strings.TrimSpace(state.EffectiveCwd)}, true
}

func worktreeModeExitMetaMessage(state session.WorktreeReminderState) (llm.Message, bool) {
	content := prompts.RenderWorktreeModeExitPrompt(state.Branch, state.EffectiveCwd, state.WorktreePath, state.WorkspaceRoot)
	if strings.TrimSpace(content) == "" {
		return llm.Message{}, false
	}
	return llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorktreeModeExit, Content: content, CompactContent: worktreeReminderCondensedText(state), SourcePath: strings.TrimSpace(state.EffectiveCwd)}, true
}

func worktreeReminderCondensedText(state session.WorktreeReminderState) string {
	effectiveCwd := strings.TrimSpace(state.EffectiveCwd)
	if effectiveCwd == "" {
		effectiveCwd = strings.TrimSpace(state.WorktreePath)
	}
	if state.Mode == session.WorktreeReminderModeExit {
		if effectiveCwd == "" {
			return "Switched worktree to main workspace"
		}
		return "Switched worktree to main workspace: " + effectiveCwd
	}
	name := strings.TrimSpace(state.Branch)
	if name == "" {
		name = strings.TrimSpace(filepath.Base(strings.TrimSpace(state.WorktreePath)))
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "worktree"
	}
	if effectiveCwd == "" {
		return "Switched worktree to " + name
	}
	return "Switched worktree to " + name + ": " + effectiveCwd
}

func skillDiscoveryWarningTexts(issues []skillDiscoveryIssue) []string {
	if len(issues) == 0 {
		return nil
	}
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, formatSkillDiscoveryWarning(issue))
	}
	return out
}

type metaContextAgentMessage struct {
	rank    int
	seq     int
	message llm.Message
}

type metaContextCollector struct {
	agentRanks          map[string]int
	nextAgentSequence   int
	seenAgentKeys       map[string]bool
	seenWarningMessages map[string]bool
	agents              []metaContextAgentMessage
	skills              *llm.Message
	subagents           *llm.Message
	environment         *llm.Message
	headless            *llm.Message
	headlessExit        *llm.Message
	workflow            *llm.Message
	worktree            *llm.Message
	worktreeExit        *llm.Message
	warnings            []string
}

func newMetaContextCollector(agentRanks map[string]int) *metaContextCollector {
	return &metaContextCollector{
		agentRanks:          agentRanks,
		seenAgentKeys:       make(map[string]bool),
		seenWarningMessages: make(map[string]bool),
	}
}

func (c *metaContextCollector) addMessages(messages []llm.Message) {
	for _, message := range messages {
		c.add(message)
	}
}

func (c *metaContextCollector) addWarnings(messages []string) {
	for _, message := range messages {
		key := strings.TrimSpace(message)
		if key == "" || c.seenWarningMessages[key] {
			continue
		}
		c.seenWarningMessages[key] = true
		c.warnings = append(c.warnings, key)
	}
}

func (c *metaContextCollector) add(message llm.Message) bool {
	classification, ok := classifyMetaContextMessage(message)
	if !ok {
		return false
	}
	message = canonicalizeMetaContextMessage(message, classification)
	if classification.key == "" {
		return false
	}
	if classification.kind == metaContextKindAgents {
		if c.seenAgentKeys[classification.key] {
			return false
		}
		c.seenAgentKeys[classification.key] = true
		rank := len(c.agentRanks) + c.nextAgentSequence
		if explicitRank, ok := c.agentRanks[classification.key]; ok {
			rank = explicitRank
		}
		c.agents = append(c.agents, metaContextAgentMessage{rank: rank, seq: c.nextAgentSequence, message: message})
		c.nextAgentSequence++
		return true
	}
	slot := c.slot(classification.kind)
	if slot == nil {
		return false
	}
	if *slot != nil {
		return false
	}
	copyMessage := message
	*slot = &copyMessage
	return true
}

func (c *metaContextCollector) slot(kind metaContextKind) **llm.Message {
	switch kind {
	case metaContextKindSkills:
		return &c.skills
	case metaContextKindSubagents:
		return &c.subagents
	case metaContextKindEnvironment:
		return &c.environment
	case metaContextKindHeadless:
		return &c.headless
	case metaContextKindHeadlessExit:
		return &c.headlessExit
	case metaContextKindWorkflow:
		return &c.workflow
	case metaContextKindWorktree:
		return &c.worktree
	case metaContextKindWorktreeExit:
		return &c.worktreeExit
	default:
		return nil
	}
}

func (c *metaContextCollector) result() metaContextBuildResult {
	sort.SliceStable(c.agents, func(i, j int) bool {
		if c.agents[i].rank != c.agents[j].rank {
			return c.agents[i].rank < c.agents[j].rank
		}
		return c.agents[i].seq < c.agents[j].seq
	})
	result := metaContextBuildResult{
		Agents:        make([]llm.Message, 0, len(c.agents)),
		SkillWarnings: append([]string(nil), c.warnings...),
	}
	for _, agent := range c.agents {
		result.Agents = append(result.Agents, agent.message)
	}
	if c.skills != nil {
		result.Skills = []llm.Message{*c.skills}
	}
	if c.subagents != nil {
		result.Subagents = []llm.Message{*c.subagents}
	}
	if c.environment != nil {
		result.Environment = []llm.Message{*c.environment}
	}
	if c.headless != nil {
		result.Headless = []llm.Message{*c.headless}
	}
	if c.headlessExit != nil {
		result.HeadlessExit = []llm.Message{*c.headlessExit}
	}
	if c.workflow != nil {
		result.Workflow = []llm.Message{*c.workflow}
	}
	if c.worktree != nil {
		result.Worktree = []llm.Message{*c.worktree}
	}
	if c.worktreeExit != nil {
		result.WorktreeExit = []llm.Message{*c.worktreeExit}
	}
	return result
}

func splitMetaContextMessages(messages []llm.Message) ([]llm.Message, []llm.Message) {
	meta := make([]llm.Message, 0, 4)
	transcript := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if _, ok := classifyMetaContextMessage(message); ok {
			meta = append(meta, message)
			continue
		}
		transcript = append(transcript, message)
	}
	return meta, transcript
}

func classifyMetaContextMessage(message llm.Message) (metaContextClassification, bool) {
	if message.Role != llm.RoleDeveloper {
		return metaContextClassification{}, false
	}
	switch message.MessageType {
	case llm.MessageTypeAgentsMD:
		sourcePath := agentSourceKey(message.SourcePath)
		if sourcePath == "" {
			return metaContextClassification{}, false
		}
		return metaContextClassification{
			kind:        metaContextKindAgents,
			key:         sourcePath,
			sourcePath:  sourcePath,
			messageType: llm.MessageTypeAgentsMD,
		}, true
	case llm.MessageTypeSkills:
		return metaContextClassification{kind: metaContextKindSkills, key: "skills", messageType: llm.MessageTypeSkills}, true
	case llm.MessageTypeSubagents:
		return metaContextClassification{kind: metaContextKindSubagents, key: "subagents", messageType: llm.MessageTypeSubagents}, true
	case llm.MessageTypeEnvironment:
		return metaContextClassification{kind: metaContextKindEnvironment, key: "environment", messageType: llm.MessageTypeEnvironment}, true
	case llm.MessageTypeHeadlessMode:
		return metaContextClassification{kind: metaContextKindHeadless, key: "headless", messageType: llm.MessageTypeHeadlessMode}, true
	case llm.MessageTypeHeadlessModeExit:
		return metaContextClassification{kind: metaContextKindHeadlessExit, key: "headless_exit", messageType: llm.MessageTypeHeadlessModeExit}, true
	case llm.MessageTypeWorkflowMode:
		return metaContextClassification{kind: metaContextKindWorkflow, key: "workflow", messageType: llm.MessageTypeWorkflowMode}, true
	case llm.MessageTypeWorktreeMode:
		return metaContextClassification{kind: metaContextKindWorktree, key: "worktree", messageType: llm.MessageTypeWorktreeMode}, true
	case llm.MessageTypeWorktreeModeExit:
		return metaContextClassification{kind: metaContextKindWorktreeExit, key: "worktree_exit", messageType: llm.MessageTypeWorktreeModeExit}, true
	}
	return metaContextClassification{}, false
}

func canonicalizeMetaContextMessage(message llm.Message, classification metaContextClassification) llm.Message {
	message.Role = llm.RoleDeveloper
	message.MessageType = classification.messageType
	return message
}

func agentSourceKey(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}
