package appfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"core/internal/testharness/scriptedllm"
	"core/server/core"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	serverstartup "core/server/startup"
	"core/server/tools"
	"core/shared/sessioncontract"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

type ScriptFile struct {
	Prompt         string                    `json:"prompt"`
	StreamDeltas   []string                  `json:"stream_deltas"`
	StreamDelayMS  *int                      `json:"stream_delay_ms"`
	Final          string                    `json:"final"`
	Steps          []StepFile                `json:"steps"`
	SeedTranscript []SeedTranscriptEntryFile `json:"seed_transcript"`
}

type SeedTranscriptEntryFile struct {
	Kind           string          `json:"kind"`
	Role           string          `json:"role"`
	Text           string          `json:"text"`
	CondensedText  string          `json:"condensed_text"`
	Visibility     string          `json:"visibility"`
	MessageType    string          `json:"message_type"`
	SourcePath     string          `json:"source_path"`
	ToolCallID     string          `json:"tool_call_id"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolOutput     json.RawMessage `json:"tool_output"`
	ToolPatch      string          `json:"tool_patch"`
	ToolSummary    string          `json:"tool_summary"`
	ToolCondensed  string          `json:"tool_condensed"`
	ToolIsError    bool            `json:"tool_is_error"`
	ToolCustom     bool            `json:"tool_custom"`
	ToolCustomText string          `json:"tool_custom_text"`
}

type StepFile struct {
	Final               string                           `json:"final"`
	Commentary          string                           `json:"commentary"`
	StreamDeltas        []string                         `json:"stream_deltas"`
	StreamDelayMS       *int                             `json:"stream_delay_ms"`
	ToolCalls           []ToolCallFile                   `json:"tool_calls"`
	ExpectedToolResults []scriptedllm.ExpectedToolResult `json:"expected_tool_results"`
}

type ToolCallFile struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Custom bool            `json:"custom"`
}

type ScriptFinalAssistantOrdinal uint64

type Runtime struct {
	ScriptFile                  ScriptFile
	Client                      *scriptedllm.Client
	recorder                    *FactoryRecorder
	targetFinalAssistantOrdinal ScriptFinalAssistantOrdinal
}

func NewRuntime(
	scriptPath string,
	newAfterResponse func(ScriptFinalAssistantOrdinal) func(context.Context) error,
) (*Runtime, error) {
	scriptFile, script, targetFinalAssistantOrdinal, err := loadScript(
		scriptPath,
		newAfterResponse,
	)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		ScriptFile:                  scriptFile,
		Client:                      scriptedllm.NewClient(script),
		recorder:                    &FactoryRecorder{},
		targetFinalAssistantOrdinal: targetFinalAssistantOrdinal,
	}, nil
}

func (r *Runtime) TargetFinalAssistantOrdinal() ScriptFinalAssistantOrdinal {
	if r == nil {
		panic("read target final assistant ordinal from nil PTY fixture runtime")
	}
	return r.targetFinalAssistantOrdinal
}

func (r *Runtime) StartupOptions() serverstartup.Options {
	return serverstartup.Options{Core: core.Options{RuntimeClientFactory: r.RuntimeClientFactory()}}
}

func (r *Runtime) RuntimeClientFactory() runtimewire.RuntimeClientFactory {
	return runtimewire.RuntimeClientFactoryFunc(func(ctx context.Context, req runtimewire.RuntimeClientRequest) (llm.Client, error) {
		r.recorder.Record(req.Purpose)
		return r.Client, nil
	})
}

func (r *Runtime) SeedSession(ctx context.Context, persistenceRoot string, workspaceRoot string) (string, error) {
	store, err := metadata.Open(persistenceRoot)
	if err != nil {
		return "", fmt.Errorf("open metadata store for seed session: %w", err)
	}
	defer func() { _ = store.Close() }()
	binding, err := store.EnsureWorkspaceBinding(ctx, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve seed workspace binding: %w", err)
	}
	sessionStore, err := session.Create(
		filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions"),
		filepath.Base(workspaceRoot),
		workspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		return "", fmt.Errorf("create seed session: %w", err)
	}
	for idx, entry := range r.ScriptFile.SeedTranscript {
		if err := appendSeedTranscriptEntry(sessionStore, workspaceRoot, idx, entry); err != nil {
			return "", err
		}
	}
	if len(r.ScriptFile.SeedTranscript) == 0 {
		if err := sessionStore.EnsureDurable(); err != nil {
			return "", fmt.Errorf("persist empty seed session: %w", err)
		}
	}
	return sessionStore.Meta().SessionID, nil
}

func (r *Runtime) Observation(runErr error) Observation {
	obs := Observation{
		FactoryPurposes:          r.recorder.Purposes(),
		ModelRequestCount:        len(r.Client.Requests()),
		RemainingScriptSteps:     r.Client.RemainingSteps(),
		StreamDeltaCount:         len(r.ScriptFile.StreamDeltas),
		FinalResponseConsumed:    r.Client.RemainingSteps() == 0 && len(r.Client.Requests()) > 0,
		DefaultProviderFallbacks: 0,
	}
	if runErr != nil {
		obs.RunError = runErr.Error()
	}
	return obs
}

func loadScript(
	path string,
	newAfterResponse func(ScriptFinalAssistantOrdinal) func(context.Context) error,
) (ScriptFile, scriptedllm.Script, ScriptFinalAssistantOrdinal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScriptFile{}, scriptedllm.Script{}, 0, fmt.Errorf("read script: %w", err)
	}
	var file ScriptFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ScriptFile{}, scriptedllm.Script{}, 0, fmt.Errorf("decode script: %w", err)
	}
	steps, err := scriptSteps(file)
	if err != nil {
		return ScriptFile{}, scriptedllm.Script{}, 0, err
	}
	targetFinalAssistantOrdinal, err := deriveTargetFinalAssistantOrdinal(steps)
	if err != nil {
		return ScriptFile{}, scriptedllm.Script{}, 0, err
	}
	if newAfterResponse != nil {
		steps[len(steps)-1].AfterResponse = newAfterResponse(targetFinalAssistantOrdinal)
	}
	return file, scriptedllm.Script{Steps: steps}, targetFinalAssistantOrdinal, nil
}

func deriveTargetFinalAssistantOrdinal(steps []scriptedllm.Step) (ScriptFinalAssistantOrdinal, error) {
	if len(steps) == 0 {
		return 0, errors.New("PTY fixture script requires at least one step")
	}
	var ordinal ScriptFinalAssistantOrdinal
	for _, step := range steps {
		if isFinalAssistantStep(step) {
			ordinal++
		}
	}
	if ordinal == 0 {
		return 0, errors.New("PTY fixture script requires an assistant final response")
	}
	if !isFinalAssistantStep(steps[len(steps)-1]) {
		return 0, errors.New("PTY fixture script must end with an assistant final response")
	}
	return ordinal, nil
}

func isFinalAssistantStep(step scriptedllm.Step) bool {
	return step.Response.Assistant.Role == llm.RoleAssistant &&
		step.Response.Assistant.Phase == llm.MessagePhaseFinal
}

func scriptSteps(file ScriptFile) ([]scriptedllm.Step, error) {
	if len(file.Steps) == 0 {
		if file.Final == "" {
			return nil, fmt.Errorf("script final response is required")
		}
		step := scriptedllm.FinalAnswer(file.Final)
		step.StreamDeltas = assistantDeltas(file.StreamDeltas)
		delay, err := streamDeltaDelay(file.StreamDelayMS)
		if err != nil {
			return nil, err
		}
		step.StreamDeltaDelay = delay
		return []scriptedllm.Step{step}, nil
	}
	steps := make([]scriptedllm.Step, 0, len(file.Steps))
	for _, spec := range file.Steps {
		step, err := scriptStep(spec)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func scriptStep(spec StepFile) (scriptedllm.Step, error) {
	var step scriptedllm.Step
	switch {
	case len(spec.ToolCalls) > 0:
		calls := make([]llm.ToolCall, 0, len(spec.ToolCalls))
		for _, call := range spec.ToolCalls {
			if call.ID == "" || call.Name == "" || len(call.Input) == 0 {
				return scriptedllm.Step{}, fmt.Errorf("tool step requires id, name, and input")
			}
			calls = append(calls, llm.ToolCall{ID: call.ID, Name: call.Name, Input: call.Input, Custom: call.Custom})
		}
		step = scriptedllm.ToolBatch(spec.Commentary, calls...)
	case spec.Final != "":
		step = scriptedllm.FinalAnswer(spec.Final)
	default:
		return scriptedllm.Step{}, fmt.Errorf("script step requires final response or tool calls")
	}
	step.StreamDeltas = assistantDeltas(spec.StreamDeltas)
	delay, err := streamDeltaDelay(spec.StreamDelayMS)
	if err != nil {
		return scriptedllm.Step{}, err
	}
	step.StreamDeltaDelay = delay
	step.ExpectedToolResults = append([]scriptedllm.ExpectedToolResult(nil), spec.ExpectedToolResults...)
	return step, nil
}

func streamDeltaDelay(milliseconds *int) (*time.Duration, error) {
	if milliseconds == nil {
		return nil, nil
	}
	if *milliseconds <= 0 {
		return nil, fmt.Errorf("stream_delay_ms must be greater than zero")
	}
	delay := time.Duration(*milliseconds) * time.Millisecond
	return &delay, nil
}

func assistantDeltas(values []string) []llm.AssistantDelta {
	deltas := make([]llm.AssistantDelta, 0, len(values))
	for _, delta := range values {
		deltas = append(deltas, llm.AssistantDelta{Text: delta, Phase: llm.MessagePhaseCommentary})
	}
	return deltas
}

func appendSeedTranscriptEntry(store *session.Store, workspaceRoot string, idx int, entry SeedTranscriptEntryFile) error {
	stepID := fmt.Sprintf("seed-%03d", idx+1)
	switch strings.TrimSpace(entry.Kind) {
	case "", "message":
		msg := seedMessage(entry)
		if _, _, err := store.AppendEvent(stepID, "message", msg); err != nil {
			return fmt.Errorf("append seed message %d: %w", idx, err)
		}
	case "local_entry":
		if _, _, err := store.AppendEvent(stepID, "local_entry", seedLocalEntry(entry)); err != nil {
			return fmt.Errorf("append seed local entry %d: %w", idx, err)
		}
	case "tool_result":
		result, message := seedToolResult(workspaceRoot, entry)
		if _, _, err := store.AppendEvent(stepID, "tool_completed", result); err != nil {
			return fmt.Errorf("append seed tool completion %d: %w", idx, err)
		}
		if _, _, err := store.AppendEvent(stepID, "message", message); err != nil {
			return fmt.Errorf("append seed tool message %d: %w", idx, err)
		}
	default:
		return fmt.Errorf("seed transcript entry %d has unknown kind %q", idx, entry.Kind)
	}
	return nil
}

func seedMessage(entry SeedTranscriptEntryFile) llm.Message {
	role := llm.Role(strings.TrimSpace(entry.Role))
	if role == "" {
		role = llm.RoleDeveloper
	}
	return llm.Message{
		Role:           role,
		MessageType:    llm.MessageType(strings.TrimSpace(entry.MessageType)),
		SourcePath:     strings.TrimSpace(entry.SourcePath),
		Content:        entry.Text,
		CompactContent: strings.TrimSpace(entry.CondensedText),
	}
}

func seedLocalEntry(entry SeedTranscriptEntryFile) seedLocalEntryPayload {
	return seedLocalEntryPayload{
		Visibility:    transcript.NormalizeEntryVisibility(transcript.EntryVisibility(entry.Visibility)),
		Role:          strings.TrimSpace(entry.Role),
		Text:          entry.Text,
		CondensedText: strings.TrimSpace(entry.CondensedText),
	}
}

type seedLocalEntryPayload struct {
	Visibility    transcript.EntryVisibility `json:"visibility,omitempty"`
	Role          string                     `json:"role"`
	Text          string                     `json:"text"`
	CondensedText string                     `json:"condensed_text,omitempty"`
	DiagnosticKey string                     `json:"diagnostic_key,omitempty"`
	NoticeID      string                     `json:"notice_id,omitempty"`
}

func seedToolResult(workspaceRoot string, entry SeedTranscriptEntryFile) (seedToolCompletionPayload, llm.Message) {
	toolName := strings.TrimSpace(entry.ToolName)
	if toolName == "" {
		toolName = strings.TrimSpace(entry.Role)
	}
	if toolName == "" {
		toolName = "tool"
	}
	callID := strings.TrimSpace(entry.ToolCallID)
	if callID == "" {
		callID = "seed_" + toolName
	}
	input := append(json.RawMessage(nil), entry.ToolInput...)
	meta := tools.BuildCallTranscriptMeta(toolName, tools.ToolCallContext{WorkingDir: workspaceRoot}, input)
	if patchText := strings.TrimSpace(entry.ToolPatch); patchText != "" {
		rendered := patchformat.Render(patchText, workspaceRoot)
		meta.PatchRender = &rendered
		meta.PatchSummary = strings.TrimSpace(rendered.SummaryText())
		meta.PatchDetail = strings.TrimSpace(rendered.DetailText())
		meta.CompactText = meta.PatchSummary
		if meta.Command == "" {
			meta.Command = meta.PatchDetail
		}
	}
	output := append(json.RawMessage(nil), entry.ToolOutput...)
	if len(output) == 0 {
		output = json.RawMessage(`{}`)
	}
	completion := seedToolCompletionPayload{
		CallID:        callID,
		Name:          toolName,
		IsError:       entry.ToolIsError,
		Output:        output,
		Summary:       strings.TrimSpace(entry.ToolSummary),
		CondensedText: strings.TrimSpace(entry.ToolCondensed),
		Presentation:  &meta,
	}
	return completion, llm.Message{
		Role:           llm.RoleTool,
		Name:           toolName,
		ToolCallID:     callID,
		Content:        string(output),
		MessageType:    llm.ToolOutputMessageType(entry.ToolCustom),
		CompactContent: strings.TrimSpace(entry.ToolCondensed),
	}
}

type seedToolCompletionPayload struct {
	CallID        string                   `json:"call_id"`
	Name          string                   `json:"name"`
	IsError       bool                     `json:"is_error"`
	Output        json.RawMessage          `json:"output"`
	Summary       string                   `json:"summary,omitempty"`
	CondensedText string                   `json:"condensed_text,omitempty"`
	Presentation  *transcript.ToolCallMeta `json:"presentation,omitempty"`
	ProviderItems []llm.ResponseItem       `json:"provider_items,omitempty"`
}

type Observation struct {
	FactoryPurposes          []string `json:"factory_purposes"`
	ModelRequestCount        int      `json:"model_request_count"`
	RemainingScriptSteps     int      `json:"remaining_script_steps"`
	StreamDeltaCount         int      `json:"stream_delta_count"`
	FinalResponseConsumed    bool     `json:"final_response_consumed"`
	DefaultProviderFallbacks int      `json:"default_provider_fallbacks"`
	RunError                 string   `json:"run_error,omitempty"`
}

func WriteObservation(path string, obs Observation) error {
	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal observations: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write observations: %w", err)
	}
	return nil
}

type FactoryRecorder struct {
	mu           sync.Mutex
	purposeNames []string
}

func (r *FactoryRecorder) Record(purpose runtimewire.RuntimeClientPurpose) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purposeNames = append(r.purposeNames, runtimeClientPurposeName(purpose))
}

func (r *FactoryRecorder) Purposes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.purposeNames...)
}

func runtimeClientPurposeName(purpose runtimewire.RuntimeClientPurpose) string {
	switch purpose {
	case runtimewire.RuntimeClientPurposeMain:
		return "main"
	case runtimewire.RuntimeClientPurposeReviewer:
		return "reviewer"
	case runtimewire.RuntimeClientPurposeWorkflow:
		return "workflow"
	default:
		return fmt.Sprintf("unknown-%d", purpose)
	}
}
