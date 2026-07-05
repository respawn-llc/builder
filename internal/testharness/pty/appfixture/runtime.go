package appfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"core/internal/testharness/scriptedllm"
	"core/server/core"
	"core/server/llm"
	"core/server/runtimewire"
	serverstartup "core/server/startup"
)

type ScriptFile struct {
	Prompt       string     `json:"prompt"`
	StreamDeltas []string   `json:"stream_deltas"`
	Final        string     `json:"final"`
	Steps        []StepFile `json:"steps"`
}

type StepFile struct {
	Final               string                           `json:"final"`
	Commentary          string                           `json:"commentary"`
	StreamDeltas        []string                         `json:"stream_deltas"`
	ToolCalls           []ToolCallFile                   `json:"tool_calls"`
	ExpectedToolResults []scriptedllm.ExpectedToolResult `json:"expected_tool_results"`
}

type ToolCallFile struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Custom bool            `json:"custom"`
}

type Runtime struct {
	ScriptFile ScriptFile
	Client     *scriptedllm.Client
	recorder   *FactoryRecorder
}

func NewRuntime(scriptPath string, afterResponse func(context.Context) error) (*Runtime, error) {
	scriptFile, script, err := loadScript(scriptPath, afterResponse)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		ScriptFile: scriptFile,
		Client:     scriptedllm.NewClient(script),
		recorder:   &FactoryRecorder{},
	}, nil
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

func loadScript(path string, afterResponse func(context.Context) error) (ScriptFile, scriptedllm.Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScriptFile{}, scriptedllm.Script{}, fmt.Errorf("read script: %w", err)
	}
	var file ScriptFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ScriptFile{}, scriptedllm.Script{}, fmt.Errorf("decode script: %w", err)
	}
	steps, err := scriptSteps(file)
	if err != nil {
		return ScriptFile{}, scriptedllm.Script{}, err
	}
	steps[len(steps)-1].AfterResponse = afterResponse
	return file, scriptedllm.Script{Steps: steps}, nil
}

func scriptSteps(file ScriptFile) ([]scriptedllm.Step, error) {
	if len(file.Steps) == 0 {
		if file.Final == "" {
			return nil, fmt.Errorf("script final response is required")
		}
		step := scriptedllm.FinalAnswer(file.Final)
		step.StreamDeltas = assistantDeltas(file.StreamDeltas)
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
	step.ExpectedToolResults = append([]scriptedllm.ExpectedToolResult(nil), spec.ExpectedToolResults...)
	return step, nil
}

func assistantDeltas(values []string) []llm.AssistantDelta {
	deltas := make([]llm.AssistantDelta, 0, len(values))
	for _, delta := range values {
		deltas = append(deltas, llm.AssistantDelta{Text: delta, Phase: llm.MessagePhaseCommentary})
	}
	return deltas
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
