package tools

import (
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const noOutputText = "No output"

func localContract(localBuilder LocalRuntimeBuilder, request RequestExposure, presentation transcript.ToolPresentationKind, renderBehavior transcript.ToolCallRenderBehavior, omitSuccessfulResult bool, buildCallMeta func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta, formatResult func(Result) string) Contract {
	return Contract{
		Runtime: RuntimeContract{Availability: RuntimeAvailabilityLocal, LocalBuilder: localBuilder},
		Request: request,
		Transcript: TranscriptContract{
			Presentation:         presentation,
			RenderBehavior:       renderBehavior,
			OmitSuccessfulResult: omitSuccessfulResult,
			BuildCallMeta:        buildCallMeta,
			FormatResult:         formatResult,
		},
	}
}

func hostedContract(request RequestExposure, presentation transcript.ToolPresentationKind, renderBehavior transcript.ToolCallRenderBehavior, omitSuccessfulResult bool, nativeWebSearch bool, buildCallMeta func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta, formatResult func(Result) string, decodeHostedOutput func(HostedToolOutput) (HostedExecution, bool)) Contract {
	return Contract{
		Runtime: RuntimeContract{
			Availability:       RuntimeAvailabilityHosted,
			NativeWebSearch:    nativeWebSearch,
			DecodeHostedOutput: decodeHostedOutput,
		},
		Request: request,
		Transcript: TranscriptContract{
			Presentation:         presentation,
			RenderBehavior:       renderBehavior,
			OmitSuccessfulResult: omitSuccessfulResult,
			BuildCallMeta:        buildCallMeta,
			FormatResult:         formatResult,
		},
	}
}

func defaultToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		command, inlineMeta := formatToolInput(toolID, raw)
		command = strings.TrimSpace(command)
		if command == "" {
			command = defaultToolCallFallback
		}
		return transcript.ToolCallMeta{
			ToolName:    string(toolID),
			Command:     command,
			CompactText: command,
			InlineMeta:  inlineMeta,
		}
	}
}

func shellToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		command, inlineMeta := formatToolInput(toolID, raw)
		command = strings.TrimSpace(command)
		if command == "" {
			command = defaultToolCallFallback
		}
		renderHint := detectShellRenderHint(ctx, toolID, raw, command)
		if toolID == toolspec.ToolWriteStdin {
			renderHint = nil
		}
		return transcript.ToolCallMeta{
			ToolName:           string(toolID),
			IsShell:            true,
			UserInitiated:      parseShellToolCallUserInitiated(raw),
			Command:            command,
			CompactText:        command,
			InlineMeta:         inlineMeta,
			TimeoutLabel:       inlineMeta,
			RenderHint:         renderHint,
			RawOutputRequested: parseShellToolCallRawOutputRequested(raw),
		}
	}
}

func askQuestionToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		question, suggestions, recommendedOptionIndex, ok := parseAskQuestionToolCall(raw)
		if !ok {
			return defaultToolCallMeta(toolID)(ctx, raw)
		}
		return transcript.ToolCallMeta{
			ToolName:               string(toolID),
			Command:                question,
			CompactText:            question,
			Question:               question,
			Suggestions:            suggestions,
			RecommendedOptionIndex: recommendedOptionIndex,
		}
	}
}

func webSearchToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		in, err := ParseWebSearchInput(raw)
		if err != nil {
			return defaultToolCallMeta(toolID)(ctx, raw)
		}
		display := FormatWebSearchDisplayText(in.Query)
		return transcript.ToolCallMeta{
			ToolName:    string(toolID),
			Command:     display,
			CompactText: display,
		}
	}
}

func triggerHandoffToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		summarizerPrompt, futureAgentMessage, ok := parseTriggerHandoffToolCall(raw)
		if !ok {
			return defaultToolCallMeta(toolID)(ctx, raw)
		}
		lines := []string{"Model requested compaction."}
		if summarizerPrompt != "" {
			lines = append(lines, "Instructions:", summarizerPrompt)
		}
		if futureAgentMessage != "" {
			lines = append(lines, "Future message:", futureAgentMessage)
		}
		return transcript.ToolCallMeta{
			ToolName:    string(toolID),
			Command:     strings.Join(lines, "\n"),
			CompactText: "Model requested compaction.",
		}
	}
}

func patchToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		detail, compact, rendered, ok := parsePatchToolCall(raw, ctx.WorkingDir)
		if !ok {
			meta := defaultToolCallMeta(toolID)(ctx, raw)
			meta.PatchSummary = meta.CompactText
			meta.PatchDetail = meta.Command
			meta.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}
			return meta
		}
		return transcript.ToolCallMeta{
			ToolName:     string(toolID),
			Command:      detail,
			CompactText:  compact,
			PatchSummary: compact,
			PatchDetail:  detail,
			PatchRender:  rendered,
			RenderHint:   &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff},
		}
	}
}

func editToolCallMeta(toolID toolspec.ID) func(ToolCallContext, json.RawMessage) transcript.ToolCallMeta {
	return func(ctx ToolCallContext, raw json.RawMessage) transcript.ToolCallMeta {
		in, err := ParseEditInput(raw)
		if err != nil {
			meta := defaultToolCallMeta(toolID)(ctx, raw)
			if path := strings.TrimSpace(in.Path); path != "" {
				meta.Command = path
				meta.CompactText = path
			}
			meta.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}
			return meta
		}
		rendered := patchformat.RenderEdit(in.Path, in.OldString, in.NewString, ctx.WorkingDir)
		detail := rendered.DetailText()
		compact := rendered.SummaryText()
		return transcript.ToolCallMeta{
			ToolName:     string(toolID),
			Command:      detail,
			CompactText:  compact,
			PatchSummary: compact,
			PatchDetail:  detail,
			PatchRender:  &rendered,
			RenderHint:   &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff},
		}
	}
}

func formatGenericToolResult(result Result) string {
	output := strings.TrimSpace(formatOutputDefault(result.Output))
	if output == "" {
		if result.IsError {
			return "tool failed"
		}
		return "done"
	}
	return output
}

func formatAskQuestionToolResult(result Result) string {
	if result.IsError {
		return formatGenericToolResult(result)
	}
	if formatted, ok := formatAskQuestionToolOutput(result.Output); ok {
		return formatted
	}
	return formatGenericToolResult(result)
}

func formatAskQuestionToolOutput(raw json.RawMessage) (string, bool) {
	var payload struct {
		Summary              string `json:"summary"`
		Answer               string `json:"answer,omitempty"`
		SelectedOptionNumber *int   `json:"selected_option_number,omitempty"`
		FreeformAnswer       string `json:"freeform_answer,omitempty"`
		Approval             *struct {
			Decision   string `json:"decision"`
			Commentary string `json:"commentary,omitempty"`
		} `json:"approval,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", false
	}
	if summary := strings.TrimSpace(payload.Summary); summary != "" {
		return summary, true
	}
	if payload.Approval != nil {
		// Legacy/internal compatibility: model-callable ask_question no longer
		// accepts approval fields, but older stored transcripts and internal
		// approval responses may still contain this shape.
		decision := strings.TrimSpace(payload.Approval.Decision)
		if decision == "" {
			return "", false
		}
		return fmt.Sprintf("User answered approval: %s.", decision), true
	}
	freeform := strings.TrimSpace(payload.FreeformAnswer)
	if freeform == "" {
		freeform = strings.TrimSpace(payload.Answer)
	}
	if payload.SelectedOptionNumber != nil {
		if *payload.SelectedOptionNumber <= 0 {
			return "", false
		}
		base := fmt.Sprintf("User answered and picked option %d.", *payload.SelectedOptionNumber)
		if freeform == "" {
			return base, true
		}
		return base + "\nUser also said:\n" + freeform, true
	}
	if freeform == "" {
		return "", false
	}
	return "User answered: " + freeform, true
}

func formatPatchToolResult(result Result) string {
	if !result.IsError {
		return ""
	}
	var payload struct {
		Error      string `json:"error"`
		Kind       string `json:"kind,omitempty"`
		Path       string `json:"path,omitempty"`
		Line       int    `json:"line,omitempty"`
		NearLine   bool   `json:"near_line,omitempty"`
		Reason     string `json:"reason,omitempty"`
		Commentary string `json:"commentary,omitempty"`
	}
	if err := json.Unmarshal(result.Output, &payload); err == nil && strings.TrimSpace(payload.Kind) != "" {
		suffix := func() string {
			if path := strings.TrimSpace(payload.Path); path != "" {
				return " in " + path
			}
			return ""
		}
		withReason := func(base string) string {
			if reason := strings.TrimSpace(payload.Reason); reason != "" {
				return base + "\nReason: " + reason
			}
			return base
		}
		switch payload.Kind {
		case "malformed_syntax":
			return withReason("Patch failed: malformed patch syntax.")
		case "content_mismatch":
			lineRef := ""
			if payload.Line > 0 {
				if payload.NearLine {
					lineRef = fmt.Sprintf(" near line %d", payload.Line)
				} else {
					lineRef = fmt.Sprintf(" at line %d", payload.Line)
				}
			}
			return withReason("Patch failed: mismatch between file content and model-provided patch" + suffix() + lineRef + ".")
		case "out_of_bounds":
			lineRef := ""
			if payload.Line > 0 {
				lineRef = fmt.Sprintf(" at line %d", payload.Line)
			}
			return withReason("Patch failed: model tried to change lines outside file bounds" + suffix() + lineRef + ".")
		case "no_permission":
			if path := strings.TrimSpace(payload.Path); path != "" {
				return withReason("Patch failed: no file edit permission for " + path + ".")
			}
			return withReason("Patch failed: no file edit permission.")
		case "user_denied":
			message := "Patch failed: user denied the edit"
			if path := strings.TrimSpace(payload.Path); path != "" {
				message += " for " + path
			}
			message += "."
			if commentary := strings.TrimSpace(payload.Commentary); commentary != "" {
				message += "\nUser said: " + commentary
			}
			return message
		case "approval_failed":
			if path := strings.TrimSpace(payload.Path); path != "" {
				return withReason("Patch failed: file edit approval failed for " + path + ".")
			}
			return withReason("Patch failed: file edit approval failed.")
		case "target_missing":
			if path := strings.TrimSpace(payload.Path); path != "" {
				return withReason("Patch failed: target file does not exist: " + path + ".")
			}
			return withReason("Patch failed: target file does not exist.")
		case "target_exists":
			if path := strings.TrimSpace(payload.Path); path != "" {
				return withReason("Patch failed: target file already exists: " + path + ".")
			}
			return withReason("Patch failed: target file already exists.")
		}
	}
	return formatGenericToolResult(result)
}

func formatEditToolResult(result Result) string {
	var message string
	if err := json.Unmarshal(result.Output, &message); err == nil {
		return strings.TrimSpace(message)
	}
	return formatGenericToolResult(result)
}

func formatViewImageToolResult(result Result) string {
	if summary, ok := formatViewImageOutput(result.Output); ok {
		return summary
	}
	return formatGenericToolResult(result)
}

func formatWebSearchToolResult(result Result) string {
	formatted := strings.TrimSpace(formatRawJSON(result.Output))
	if formatted != "" {
		return formatted
	}
	return formatGenericToolResult(result)
}

func formatTriggerHandoffToolResult(result Result) string {
	if result.IsError {
		return formatGenericToolResult(result)
	}
	var text string
	if err := json.Unmarshal(result.Output, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var payload struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(result.Output, &payload); err == nil {
		if summary := strings.TrimSpace(payload.Summary); summary != "" {
			return summary
		}
	}
	return ""
}

func parseShellToolCallUserInitiated(raw json.RawMessage) bool {
	var in struct {
		UserInitiated bool `json:"user_initiated"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return false
	}
	return in.UserInitiated
}

func parseShellToolCallRawOutputRequested(raw json.RawMessage) bool {
	var in struct {
		Raw bool `json:"raw"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return false
	}
	return in.Raw
}

func parseAskQuestionToolCall(raw json.RawMessage) (string, []string, int, bool) {
	var in struct {
		Question               string   `json:"question"`
		Suggestions            []string `json:"suggestions,omitempty"`
		RecommendedOptionIndex int      `json:"recommended_option_index,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", nil, 0, false
	}
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return "", nil, 0, false
	}
	suggestions := normalizeAskQuestionSuggestions(in.Suggestions)
	recommendedOptionIndex := in.RecommendedOptionIndex
	if recommendedOptionIndex < 1 || recommendedOptionIndex > len(suggestions) {
		recommendedOptionIndex = 0
	}
	return question, suggestions, recommendedOptionIndex, true
}

func normalizeAskQuestionSuggestions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, suggestion := range in {
		trimmed := strings.TrimSpace(suggestion)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseTriggerHandoffToolCall(raw json.RawMessage) (string, string, bool) {
	var in struct {
		SummarizerPrompt   string `json:"summarizer_prompt,omitempty"`
		FutureAgentMessage string `json:"future_agent_message,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", "", false
	}
	return strings.TrimSpace(in.SummarizerPrompt), strings.TrimSpace(in.FutureAgentMessage), true
}

func parsePatchToolCall(raw json.RawMessage, cwd string) (detail string, compact string, rendered *patchformat.RenderedPatch, ok bool) {
	var patchText string
	if err := json.Unmarshal(raw, &patchText); err != nil {
		var payload struct {
			Patch string `json:"patch"`
		}
		if payloadErr := json.Unmarshal(raw, &payload); payloadErr != nil {
			return "", "", nil, false
		}
		patchText = payload.Patch
	}
	trimmedPatch := strings.TrimSpace(patchText)
	if trimmedPatch == "" {
		return "", "", nil, false
	}
	r := patchformat.Render(patchText, cwd)
	return r.DetailText(), r.SummaryText(), &r, true
}

func detectShellRenderHint(ctx ToolCallContext, toolID toolspec.ID, raw json.RawMessage, command string) *transcript.ToolRenderHint {
	defaultHint := &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindShell, ShellDialect: detectToolShellDialect(ctx, toolID, raw)}
	args, ok := ParseSimpleShellCommand(command)
	if !ok || len(args) == 0 {
		return defaultHint
	}

	name := NormalizeShellCommandName(args[0])
	switch name {
	case "cat":
		filePath, ok := parseCatFileArg(args)
		if !ok {
			return defaultHint
		}
		return &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindSource, Path: filePath, ResultOnly: true}
	case "nl":
		filePath, ok := parseNlFileArg(args)
		if !ok {
			return defaultHint
		}
		return &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindSource, Path: filePath, ResultOnly: true}
	case "sed":
		filePath, ok := parseSedFileArg(args)
		if !ok {
			return defaultHint
		}
		return &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindSource, Path: filePath, ResultOnly: true}
	default:
		return defaultHint
	}
}

func detectToolShellDialect(ctx ToolCallContext, toolID toolspec.ID, raw json.RawMessage) transcript.ToolShellDialect {
	if toolID == toolspec.ToolExecCommand {
		if shellPath := parseRequestedExecShell(raw); shellPath != "" {
			if dialect, ok := shellDialectForExecutable(shellPath); ok {
				return dialect
			}
		}
	}
	if shellPath := strings.TrimSpace(ctx.DefaultShellPath); shellPath != "" {
		if dialect, ok := shellDialectForExecutable(shellPath); ok {
			return dialect
		}
	}
	if strings.EqualFold(strings.TrimSpace(ctx.GOOS), "windows") {
		return transcript.ToolShellDialectWindowsCommand
	}
	return transcript.ToolShellDialectPosix
}

func parseRequestedExecShell(raw json.RawMessage) string {
	var in struct {
		Shell string `json:"shell,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	return strings.TrimSpace(in.Shell)
}

func shellDialectForExecutable(shellPath string) (transcript.ToolShellDialect, bool) {
	name := shellExecutableName(shellPath)
	switch name {
	case "pwsh", "powershell":
		return transcript.ToolShellDialectPowerShell, true
	case "cmd", "command":
		return transcript.ToolShellDialectWindowsCommand, true
	case "sh", "bash", "zsh", "dash", "ash", "ksh", "mksh", "fish", "nu", "nushell":
		return transcript.ToolShellDialectPosix, true
	default:
		return "", false
	}
}

func shellExecutableName(shellPath string) string {
	trimmed := strings.TrimSpace(shellPath)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	base := path.Base(trimmed)
	if base == "." || base == "/" {
		base = filepath.Base(trimmed)
	}
	base = strings.ToLower(strings.TrimSpace(base))
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

func parseCatFileArg(args []string) (string, bool) {
	if len(args) == 2 && !strings.HasPrefix(args[1], "-") {
		return args[1], true
	}
	if len(args) == 3 && args[1] == "--" && strings.TrimSpace(args[2]) != "" {
		return args[2], true
	}
	return "", false
}

func parseNlFileArg(args []string) (string, bool) {
	if len(args) == 2 && !strings.HasPrefix(args[1], "-") {
		return args[1], true
	}
	if len(args) == 3 && (args[1] == "-ba" || args[1] == "--body-numbering=a") && strings.TrimSpace(args[2]) != "" {
		return args[2], true
	}
	if len(args) == 4 && (args[1] == "-ba" || args[1] == "--body-numbering=a") && args[2] == "--" && strings.TrimSpace(args[3]) != "" {
		return args[3], true
	}
	return "", false
}

func parseSedFileArg(args []string) (string, bool) {
	if len(args) < 4 || args[1] != "-n" || !isSedPrintRangeExpression(args[2]) {
		return "", false
	}

	if len(args) == 4 {
		if strings.HasPrefix(args[3], "-") {
			return "", false
		}
		return args[3], true
	}

	if len(args) == 5 && args[3] == "--" && strings.TrimSpace(args[4]) != "" {
		return args[4], true
	}

	return "", false
}

func isSedPrintRangeExpression(expression string) bool {
	if len(expression) < 2 || expression[len(expression)-1] != 'p' {
		return false
	}
	rangeExpression := expression[:len(expression)-1]
	seenComma := false
	segmentHasDigit := false
	for index := 0; index < len(rangeExpression); index++ {
		character := rangeExpression[index]
		if character >= '0' && character <= '9' {
			segmentHasDigit = true
			continue
		}
		if character != ',' || seenComma || !segmentHasDigit {
			return false
		}
		seenComma = true
		segmentHasDigit = false
	}
	return segmentHasDigit
}

func decodeHostedWebSearchOutput(item HostedToolOutput) (HostedExecution, bool) {
	raw := item.Raw
	if len(raw) == 0 || !json.Valid(raw) {
		return HostedExecution{}, false
	}
	var payload struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Status string `json:"status"`
		Action struct {
			Type    string `json:"type"`
			Query   string `json:"query"`
			URL     string `json:"url"`
			Pattern string `json:"pattern"`
		} `json:"action"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return HostedExecution{}, false
	}
	if strings.TrimSpace(payload.Type) != "web_search_call" {
		return HostedExecution{}, false
	}
	callID := strings.TrimSpace(payload.ID)
	if callID == "" {
		callID = strings.TrimSpace(item.ID)
	}
	if callID == "" {
		callID = strings.TrimSpace(item.CallID)
	}
	if callID == "" {
		return HostedExecution{}, false
	}
	input := map[string]string{}
	actionType := strings.TrimSpace(payload.Action.Type)
	if actionType != "" {
		input["action"] = actionType
	}
	query := strings.TrimSpace(payload.Action.Query)
	searchQuery := query
	if url := strings.TrimSpace(payload.Action.URL); url != "" {
		if query == "" {
			query = url
		}
		input["url"] = url
	}
	if pattern := strings.TrimSpace(payload.Action.Pattern); pattern != "" {
		if query == "" {
			query = pattern
		}
		input["pattern"] = pattern
	}
	if strings.EqualFold(actionType, "search") {
		input["query"] = searchQuery
	} else if query != "" || searchQuery != "" {
		input["query"] = query
	} else if query == "" {
		if actionType != "" {
			query = actionType
		} else {
			query = "web search"
		}
		input["query"] = query
	}
	inputRaw, err := json.Marshal(input)
	if err != nil {
		return HostedExecution{}, false
	}
	output := append(json.RawMessage(nil), raw...)
	if !json.Valid(output) {
		output = mustJSON(map[string]any{"raw": string(raw)})
	}
	isError := strings.EqualFold(strings.TrimSpace(payload.Status), "failed")
	if strings.EqualFold(actionType, "search") {
		if err := ValidateWebSearchQuery(searchQuery); err != nil {
			output = mustJSON(map[string]any{"error": InvalidWebSearchQueryMessage})
			isError = true
		}
	}
	return HostedExecution{
		Call: HostedCall{
			ID:    callID,
			Name:  toolspec.ToolWebSearch,
			Input: inputRaw,
		},
		Result: Result{
			CallID:  callID,
			Name:    toolspec.ToolWebSearch,
			Output:  output,
			IsError: isError,
		},
	}, true
}

func formatToolInput(toolID toolspec.ID, raw json.RawMessage) (string, string) {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return strings.TrimSpace(string(raw)), ""
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return renderPlain(payload), ""
	}
	if toolID == toolspec.ToolWriteStdin {
		sessionID, _ := asInt(obj["session_id"])
		chars, _ := asString(obj["chars"])
		if strings.TrimSpace(chars) == "" {
			if yieldTimeMS, ok := asInt(obj["yield_time_ms"]); ok && yieldTimeMS > 0 {
				pollDuration := time.Duration(yieldTimeMS) * time.Millisecond
				pollDurationText := "0s"
				if pollDuration > 0 {
					pollDurationText = pollDuration.String()
				}
				return fmt.Sprintf("Polled session %d for %s", sessionID, pollDurationText), ""
			}
			return fmt.Sprintf("poll session %d", sessionID), ""
		}
		return fmt.Sprintf("write stdin session %d", sessionID), ""
	}
	if toolID == toolspec.ToolExecCommand {
		if cmd, ok := asString(obj["cmd"]); ok {
			return cmd, ""
		}
		if cmd, ok := asString(obj["command"]); ok {
			return cmd, ""
		}
	}
	if cmd, ok := asString(obj["command"]); ok {
		inlineMeta := ""
		if secs, ok := asInt(obj["timeout_seconds"]); ok && secs > 0 {
			inlineMeta = "timeout: " + formatDurationShort(time.Duration(secs)*time.Second)
		}
		return cmd, inlineMeta
	}
	if toolID == toolspec.ToolWebSearch {
		if query, ok := asString(obj["query"]); ok {
			return FormatWebSearchDisplayText(query), ""
		}
		return FormatWebSearchDisplayText(""), ""
	}
	if toolID == toolspec.ToolAskQuestion {
		if question, ok := asString(obj["question"]); ok {
			return question, ""
		}
	}
	if toolID == toolspec.ToolTriggerHandoff {
		summarizerPrompt, futureAgentMessage, ok := parseTriggerHandoffToolCall(raw)
		if !ok {
			return "trigger_handoff()", ""
		}
		command := "trigger_handoff()"
		var notes []string
		if summarizerPrompt != "" {
			notes = append(notes, "custom summarizer prompt")
		}
		if futureAgentMessage != "" {
			notes = append(notes, "future-agent message")
		}
		if len(notes) == 0 {
			return command, ""
		}
		return command + " with " + strings.Join(notes, ", "), ""
	}
	return renderPlain(payload), ""
}

func formatOutputDefault(raw json.RawMessage) string {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return strings.TrimSpace(string(raw))
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		formatted, err := json.Marshal(payload)
		if err != nil {
			return strings.TrimSpace(string(raw))
		}
		return string(formatted)
	}

	if msg, ok := asString(obj["error"]); ok {
		return msg
	}
	if out, ok := asString(obj["output"]); ok {
		out = strings.TrimSpace(out)
		if out == "" {
			out = noOutputText
		}
		var notes []string
		if code, ok := asInt(obj["exit_code"]); ok && code != 0 {
			notes = append(notes, fmt.Sprintf("exit code %d", code))
		}
		if len(notes) == 0 {
			return out
		}
		return out + "\n" + strings.Join(notes, ", ")
	}
	if answer, ok := asString(obj["answer"]); ok {
		return answer
	}
	formatted, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(formatted)
}

func formatViewImageOutput(raw json.RawMessage) (string, bool) {
	var items []struct {
		Type     string `json:"type"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", false
	}
	if len(items) == 0 {
		return "", false
	}

	labels := make([]string, 0, len(items))
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "input_image":
			labels = append(labels, "attached image")
		case "input_file":
			filename := strings.TrimSpace(item.Filename)
			if filename == "" {
				labels = append(labels, "attached PDF")
				continue
			}
			labels = append(labels, "attached PDF: "+filename)
		default:
			labels = append(labels, "attached multimodal content")
		}
	}
	if len(labels) == 0 {
		return "", false
	}
	return strings.Join(labels, "\n"), true
}

func formatRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if !json.Valid(raw) {
		return strings.TrimSpace(string(raw))
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return strings.TrimSpace(string(raw))
	}
	formatted, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(formatted)
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func formatDurationShort(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	total := int(d.Seconds())
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60

	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, "")
}

func renderPlain(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		if len(x) == 0 {
			return "[]"
		}
		lines := make([]string, 0, len(x))
		for _, item := range x {
			rendered := strings.TrimSpace(renderPlain(item))
			if rendered == "" {
				continue
			}
			itemLines := strings.Split(rendered, "\n")
			lines = append(lines, "- "+itemLines[0])
			for _, line := range itemLines[1:] {
				lines = append(lines, "  "+line)
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, k := range keys {
			rendered := strings.TrimSpace(renderPlain(x[k]))
			if rendered == "" {
				lines = append(lines, k+":")
				continue
			}
			valueLines := strings.Split(rendered, "\n")
			lines = append(lines, k+": "+valueLines[0])
			for _, line := range valueLines[1:] {
				lines = append(lines, "  "+line)
			}
		}
		return strings.Join(lines, "\n")
	default:
		return fmt.Sprintf("%v", x)
	}
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	default:
		return 0, false
	}
}
