package tools

import (
	"embed"
	"encoding/json"
	"sort"

	"core/shared/toolspec"
	"core/shared/transcript"
)

type CatalogEntry struct {
	ID             toolspec.ID
	Aliases        []string
	Description    string
	Schema         json.RawMessage
	DefaultEnabled bool
	Contract       Contract
}

//go:embed schemas/*.json
var toolSchemaFS embed.FS

func mustToolSchema(name string) json.RawMessage {
	data, err := toolSchemaFS.ReadFile("schemas/" + name)
	if err != nil {
		panic("read tool schema " + name + ": " + err.Error())
	}
	return json.RawMessage(data)
}

var catalogEntries = []CatalogEntry{
	{
		ID:             toolspec.ToolExecCommand,
		Aliases:        []string{"bash", "bash_command", "shell", "shell_command"},
		Description:    "Runs a command in the user's default shell, returning output or an ID of a background process.",
		DefaultEnabled: true,
		Contract: localContract(
			LocalRuntimeBuilderExecCommand,
			RequestExposure{Enabled: true},
			transcript.ToolPresentationShell,
			transcript.ToolCallRenderBehaviorShell,
			false,
			shellToolCallMeta(toolspec.ToolExecCommand),
			formatGenericToolResult,
		),
		Schema: mustToolSchema("exec_command.json"),
	},
	{
		ID:             toolspec.ToolWriteStdin,
		Aliases:        nil,
		Description:    "Writes characters to an existing exec_command session and returns recent output. Use empty chars to poll.",
		DefaultEnabled: true,
		Contract: localContract(
			LocalRuntimeBuilderWriteStdin,
			RequestExposure{Enabled: true},
			transcript.ToolPresentationShell,
			transcript.ToolCallRenderBehaviorShell,
			false,
			shellToolCallMeta(toolspec.ToolWriteStdin),
			formatGenericToolResult,
		),
		Schema: mustToolSchema("write_stdin.json"),
	},
	{
		ID:             toolspec.ToolViewImage,
		Aliases:        []string{"read_image"},
		Description:    "View a local PNG, JPEG, still GIF, or PDF file by path. You will see PDFs as images (not OCR/text).",
		DefaultEnabled: true,
		Contract: localContract(
			LocalRuntimeBuilderViewImage,
			RequestExposure{Enabled: true, RequiresVision: true},
			transcript.ToolPresentationDefault,
			transcript.ToolCallRenderBehaviorDefault,
			false,
			viewImageToolCallMeta(toolspec.ToolViewImage),
			formatViewImageToolResult,
		),
		Schema: mustToolSchema("view_image.json"),
	},
	{
		ID:             toolspec.ToolPatch,
		Aliases:        nil,
		Description:    "Apply edits to files using freeform patch syntax.",
		DefaultEnabled: true,
		Contract: localContract(
			LocalRuntimeBuilderPatch,
			RequestExposure{Enabled: true},
			transcript.ToolPresentationDefault,
			transcript.ToolCallRenderBehaviorDefault,
			true,
			patchToolCallMeta(toolspec.ToolPatch),
			formatPatchToolResult,
		),
		Schema: mustToolSchema("patch.json"),
	},
	{
		ID:             toolspec.ToolEdit,
		Aliases:        []string{"replace", "write"},
		Description:    "Replace text in a file, create a missing or empty file, or delete matched text. old_string should match current file content and include enough context to be unique.",
		DefaultEnabled: false,
		Contract: localContract(
			LocalRuntimeBuilderEdit,
			RequestExposure{Enabled: true},
			transcript.ToolPresentationDefault,
			transcript.ToolCallRenderBehaviorDefault,
			true,
			editToolCallMeta(toolspec.ToolEdit),
			formatEditToolResult,
		),
		Schema: mustToolSchema("edit.json"),
	},
	{
		ID:             toolspec.ToolAskQuestion,
		Aliases:        nil,
		Description:    "Ask the user a question. You should ask the user when planning or working to make product decisions, resolve ambiguities, define missing pieces that you cannot resolve by yourself, brainstorming with the user. You should ask the user a lot of questions when you're planning/brainstorming together to learn their desires, preferences, design, product vision, architecture, and sometimes ask them questions when already working if you encounter a problem you can't resolve, a caveat, an undefined area that materially affects the result or direction of your work, etc. You should avoid asking the user obvious or harmless questions like 'Should I run tests?' or 'Where is file X?' which you can answer yourself. Stick to ONE question per this tool call, for multiple questions call this tool in parallel. Strive to provide multiple suggestions/options with every question if applicable, and providing one recommended option you deem best for user goals. Prefer including all the context necessary to answer a question in the question text (it's fine if it becomes large), rather than using commentary to provide it.",
		DefaultEnabled: true,
		Contract: localContract(
			LocalRuntimeBuilderAskQuestion,
			RequestExposure{Enabled: true},
			transcript.ToolPresentationAskQuestion,
			transcript.ToolCallRenderBehaviorAskQuestion,
			false,
			askQuestionToolCallMeta(toolspec.ToolAskQuestion),
			formatAskQuestionToolResult,
		),
		Schema: mustToolSchema("ask_question.json"),
	},
	{
		ID:             toolspec.ToolCompleteNode,
		Aliases:        nil,
		Description:    "Mark your task as completed in workflow scenarios. Use this tool exactly as described in the workflow task developer message and only when the task is fully complete.",
		DefaultEnabled: false,
		Contract: localContract(
			LocalRuntimeBuilderCompleteNode,
			RequestExposure{Enabled: true, RequiresCurrentNodeExecution: true},
			transcript.ToolPresentationDefault,
			transcript.ToolCallRenderBehaviorDefault,
			false,
			defaultToolCallMeta(toolspec.ToolCompleteNode),
			formatGenericToolResult,
		),
		// Runtime requests replace this fallback with the current workflow
		// run contract, including valid transitions and parameters.
		Schema: mustToolSchema("complete_node.json"),
	},
	{
		ID:             toolspec.ToolTriggerHandoff,
		Aliases:        nil,
		Description:    "Trigger a proactive handoff to the next agent. By default, this tool is disallowed even if visible. Using it is allowed only after a specific developer message appears in the transcript that allows this tool. Do not use this tool before the reminder. The tool is private to you, so you can use 'analysis' channel content in its parameters.",
		DefaultEnabled: true,
		Contract: localContract(
			LocalRuntimeBuilderTriggerHandoff,
			RequestExposure{Enabled: true},
			transcript.ToolPresentationDefault,
			transcript.ToolCallRenderBehaviorDefault,
			false,
			triggerHandoffToolCallMeta(toolspec.ToolTriggerHandoff),
			formatTriggerHandoffToolResult,
		),
		Schema: mustToolSchema("trigger_handoff.json"),
	},
	{
		ID:             toolspec.ToolWebSearch,
		Aliases:        nil,
		Description:    "Search the web for up-to-date external information. Use this when local workspace context is insufficient, the fact could be stale, or for information beyond your model knowledge cutoff. Prefer primary and official sources.",
		DefaultEnabled: true,
		Contract: hostedContract(
			RequestExposure{Enabled: false},
			transcript.ToolPresentationDefault,
			transcript.ToolCallRenderBehaviorDefault,
			false,
			true,
			webSearchToolCallMeta(toolspec.ToolWebSearch),
			formatWebSearchToolResult,
			decodeHostedWebSearchOutput,
		),
		Schema: mustToolSchema("web_search.json"),
	},
}

var (
	definitions       map[toolspec.ID]Definition
	parseAliases      map[string]toolspec.ID
	catalogIDs        []toolspec.ID
	defaultEnabledIDs []toolspec.ID
)

func init() {
	definitions = make(map[toolspec.ID]Definition, len(catalogEntries))
	parseAliases = make(map[string]toolspec.ID, len(catalogEntries)*2)
	catalogIDs = make([]toolspec.ID, 0, len(catalogEntries))
	defaultEnabledIDs = make([]toolspec.ID, 0, len(catalogEntries))

	for _, entry := range catalogEntries {
		validateCatalogEntry(entry)
		definitions[entry.ID] = Definition{
			ID:          entry.ID,
			Description: entry.Description,
			Schema:      entry.Schema,
			contract:    entry.Contract,
		}
		parseAliases[string(entry.ID)] = entry.ID
		for _, alias := range entry.Aliases {
			parseAliases[alias] = entry.ID
		}
		catalogIDs = append(catalogIDs, entry.ID)
		if entry.DefaultEnabled {
			defaultEnabledIDs = append(defaultEnabledIDs, entry.ID)
		}
	}

	sort.Slice(catalogIDs, func(i, j int) bool { return catalogIDs[i] < catalogIDs[j] })
	sort.Slice(defaultEnabledIDs, func(i, j int) bool { return defaultEnabledIDs[i] < defaultEnabledIDs[j] })
}

func CatalogIDs() []toolspec.ID {
	out := make([]toolspec.ID, len(catalogIDs))
	copy(out, catalogIDs)
	return out
}

func DefaultEnabledToolIDs() []toolspec.ID {
	out := make([]toolspec.ID, len(defaultEnabledIDs))
	copy(out, defaultEnabledIDs)
	return out
}

func validateCatalogEntry(entry CatalogEntry) {
	if entry.Contract.Runtime.Availability == "" {
		panic("tool contract is missing runtime availability for " + string(entry.ID))
	}
	if entry.Contract.Runtime.Availability == RuntimeAvailabilityHosted && entry.Contract.Runtime.DecodeHostedOutput == nil {
		panic("hosted tool contract is missing hosted output decoder for " + string(entry.ID))
	}
	if entry.Contract.Runtime.Availability == RuntimeAvailabilityLocal && entry.Contract.Runtime.LocalBuilder == "" {
		panic("local tool contract is missing local runtime builder for " + string(entry.ID))
	}
	if entry.Contract.Runtime.Availability == RuntimeAvailabilityHosted && entry.Contract.Runtime.LocalBuilder != "" {
		panic("hosted tool contract must not declare a local runtime builder for " + string(entry.ID))
	}
	if entry.Contract.Transcript.BuildCallMeta == nil {
		panic("tool contract is missing transcript call metadata builder for " + string(entry.ID))
	}
	if entry.Contract.Transcript.FormatResult == nil {
		panic("tool contract is missing transcript result formatter for " + string(entry.ID))
	}
	if entry.Contract.Transcript.Presentation == "" {
		panic("tool contract is missing transcript presentation for " + string(entry.ID))
	}
	if entry.Contract.Transcript.RenderBehavior == "" {
		panic("tool contract is missing transcript render behavior for " + string(entry.ID))
	}
}
