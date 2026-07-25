package tools

import (
	"encoding/json"
	"strings"

	"core/shared/toolspec"
	"core/shared/transcript"
)

const (
	InlineMetaSeparator     = transcript.InlineMetaSeparator
	defaultToolCallFallback = "tool call"
)

type RuntimeAvailability string

const (
	RuntimeAvailabilityLocal  RuntimeAvailability = "local"
	RuntimeAvailabilityHosted RuntimeAvailability = "hosted"
)

type RequestExposure struct {
	Enabled                      bool
	RequiresVision               bool
	RequiresCurrentNodeExecution bool
}

type RequestExposureContext struct {
	SupportsVision     bool
	WorkflowCompletion bool
}

func (r RequestExposure) Allowed(ctx RequestExposureContext) bool {
	if !r.Enabled {
		return false
	}
	if r.RequiresVision && !ctx.SupportsVision {
		return false
	}
	if r.RequiresCurrentNodeExecution && !ctx.WorkflowCompletion {
		return false
	}
	return true
}

type HostedToolOutput struct {
	ID     string
	CallID string
	Raw    json.RawMessage
}

type HostedCall struct {
	ID    string
	Name  toolspec.ID
	Input json.RawMessage
}

type HostedExecution struct {
	Call   HostedCall
	Result Result
}

type ToolCallContext struct {
	WorkingDir       string
	DefaultShellPath string
	GOOS             string
}

type TranscriptContract struct {
	Presentation         transcript.ToolPresentationKind
	RenderBehavior       transcript.ToolCallRenderBehavior
	OmitSuccessfulResult bool
	BuildCallMeta        func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta
	FormatResult         func(Result) string
}

type LocalRuntimeBuilder string

const (
	LocalRuntimeBuilderExecCommand    LocalRuntimeBuilder = "exec_command"
	LocalRuntimeBuilderWriteStdin     LocalRuntimeBuilder = "write_stdin"
	LocalRuntimeBuilderViewImage      LocalRuntimeBuilder = "view_image"
	LocalRuntimeBuilderPatch          LocalRuntimeBuilder = "patch"
	LocalRuntimeBuilderEdit           LocalRuntimeBuilder = "edit"
	LocalRuntimeBuilderAskQuestion    LocalRuntimeBuilder = "ask_question"
	LocalRuntimeBuilderCompleteNode   LocalRuntimeBuilder = "complete_node"
	LocalRuntimeBuilderTriggerHandoff LocalRuntimeBuilder = "trigger_handoff"
)

type RuntimeContract struct {
	Availability       RuntimeAvailability
	NativeWebSearch    bool
	LocalBuilder       LocalRuntimeBuilder
	DecodeHostedOutput func(HostedToolOutput) (HostedExecution, bool)
}

type Contract struct {
	Runtime    RuntimeContract
	Request    RequestExposure
	Transcript TranscriptContract
}

func (d Definition) AvailableInLocalRuntime() bool {
	return d.contract.Runtime.Availability == RuntimeAvailabilityLocal
}

func (d Definition) LocalRuntimeBuilder() LocalRuntimeBuilder {
	return d.contract.Runtime.LocalBuilder
}

func (d Definition) BuildToolCallMeta(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
	meta := transcript.ToolCallMeta{ToolName: string(d.ID)}
	if d.contract.Transcript.BuildCallMeta != nil {
		meta = d.contract.Transcript.BuildCallMeta(ctx, raw)
	}
	meta.ToolName = string(d.ID)
	if meta.Presentation == "" {
		meta.Presentation = d.contract.Transcript.Presentation
	}
	if meta.RenderBehavior == "" {
		meta.RenderBehavior = d.contract.Transcript.RenderBehavior
	}
	if strings.TrimSpace(meta.CompactText) == "" {
		meta.CompactText = strings.TrimSpace(meta.Command)
	}
	if strings.TrimSpace(meta.TimeoutLabel) == "" {
		meta.TimeoutLabel = strings.TrimSpace(meta.InlineMeta)
	}
	if d.contract.Transcript.OmitSuccessfulResult {
		meta.OmitSuccessfulResult = true
	}
	return transcript.NormalizeToolCallMeta(meta)
}

func (d Definition) FormatToolInput(ctx ToolCallContext, raw json.RawMessage) (string, string) {
	meta := d.BuildToolCallMeta(ctx, raw)
	return strings.TrimSpace(meta.Command), strings.TrimSpace(meta.InlineMeta)
}

func (d Definition) FormatToolResult(result Result) string {
	if d.contract.Transcript.FormatResult == nil {
		return strings.TrimSpace(string(result.Output))
	}
	return d.contract.Transcript.FormatResult(result)
}

func (d Definition) DecodeHostedOutput(item HostedToolOutput) (HostedExecution, bool) {
	if d.contract.Runtime.DecodeHostedOutput == nil {
		return HostedExecution{}, false
	}
	return d.contract.Runtime.DecodeHostedOutput(item)
}

func (d Definition) EnablesNativeWebSearch(mode string) bool {
	return d.contract.Runtime.NativeWebSearch && strings.EqualFold(strings.TrimSpace(mode), "native")
}

func DefinitionFor(id toolspec.ID) (Definition, bool) {
	def, ok := definitions[id]
	return def, ok
}

func definitionForToolName(toolName string) (Definition, bool) {
	id, ok := parseAliases[strings.TrimSpace(toolName)]
	if !ok {
		return Definition{}, false
	}
	def, ok := definitions[id]
	return def, ok
}

func fallbackToolCallMeta(toolName string, raw json.RawMessage) transcript.ToolCallMeta {
	command := strings.TrimSpace(string(raw))
	if command == "" {
		command = defaultToolCallFallback
	}
	return transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{
		ToolName:       strings.TrimSpace(toolName),
		Presentation:   transcript.ToolPresentationDefault,
		RenderBehavior: transcript.ToolCallRenderBehaviorDefault,
		Command:        command,
		CompactText:    command,
	})
}

func BuildCallTranscriptMeta(toolName string, ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
	def, ok := definitionForToolName(toolName)
	if !ok {
		return fallbackToolCallMeta(toolName, raw)
	}
	return def.BuildToolCallMeta(ctx, raw)
}

func FormatToolResultByName(toolName string, raw json.RawMessage, isError bool) string {
	def, ok := definitionForToolName(toolName)
	if ok {
		return def.FormatToolResult(Result{Name: def.ID, Output: raw, IsError: isError})
	}
	output := strings.TrimSpace(formatOutputDefault(raw))
	if output == "" {
		if isError {
			return "tool failed"
		}
		return "done"
	}
	return output
}

func DefinitionsFor(ids []toolspec.ID) []Definition {
	defs := make([]Definition, 0, len(ids))
	seen := make(map[toolspec.ID]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		def, ok := definitions[id]
		if !ok {
			continue
		}
		defs = append(defs, def)
	}
	return defs
}

func FilterRequestExposedDefinitions(defs []Definition, ctx RequestExposureContext) []Definition {
	out := make([]Definition, 0, len(defs))
	for _, def := range defs {
		if def.contract.Request.Allowed(ctx) {
			out = append(out, def)
		}
	}
	return out
}

func RequestExposedDefinitionsForSession(enabled []toolspec.ID, registered []Definition, ctx RequestExposureContext) []Definition {
	if len(enabled) > 0 {
		return FilterRequestExposedDefinitions(DefinitionsFor(enabled), ctx)
	}
	return FilterRequestExposedDefinitions(registered, ctx)
}

func NeedsNativeWebSearch(ids []toolspec.ID, mode string) bool {
	for _, def := range DefinitionsFor(ids) {
		if def.EnablesNativeWebSearch(mode) {
			return true
		}
	}
	return false
}

func HostedExecutionsFromOutputs(items []HostedToolOutput, defs []Definition) []HostedExecution {
	if len(items) == 0 || len(defs) == 0 {
		return nil
	}
	out := make([]HostedExecution, 0, len(items))
	for _, item := range items {
		for _, def := range defs {
			execution, ok := def.DecodeHostedOutput(item)
			if !ok {
				continue
			}
			out = append(out, execution)
			break
		}
	}
	return out
}
