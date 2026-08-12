package runtimewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/runtimewirefixture"
	"core/internal/testharness/scriptedllm"
	"core/internal/testharness/testsetup"
	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/shared/config"
	"core/shared/imagefileio"
	"core/shared/jsoncontract"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"core/shared/sessioncontract"
)

func TestMain(m *testing.M) {
	imagefileio.ExitIfWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	os.Exit(m.Run())
}

type mismatchedDeletionPresentationHandler struct{}

func (mismatchedDeletionPresentationHandler) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	received := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 9}
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: received},
				OperationIDs:  []patchformat.WholeFileDeletionOperationID{received},
				Removed:       3,
			}},
		},
	}, nil
}

func TestRuntimeWiringSnapshotsActiveDebugSettingForToolCompletionMismatch(t *testing.T) {
	const childProcess = "KENT_RUNTIMEWIRE_DEBUG_MISMATCH_CHILD"
	if os.Getenv(childProcess) == "" {
		command := exec.Command(
			os.Args[0],
			"-test.run",
			"TestRuntimeWiringSnapshotsActiveDebugSettingForToolCompletionMismatch",
		)
		command.Env = append(os.Environ(), childProcess+"=1")
		output, err := command.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("debug mismatch child process exit = %v, want panic exit code 2\n%s", err, output)
		}
		return
	}

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "debug-mismatch")
	call := llm.ToolCall{
		ID:          "c5928052-8654-41eb-819e-b9d7e3f5200e",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value("*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"),
	}
	client := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{scriptedllm.ToolBatch("", call)},
	})
	active := runtimeWireShellSettings(config.ShellPostprocessingModeBuiltin, nil)
	active.Debug = true
	wiring, err := NewRuntimeWiringWithBackground(
		store,
		materializedRuntimeWireEventLog(t, store),
		active,
		[]toolspec.ID{toolspec.ToolPatch},
		nil,
		nil,
		nil,
		RuntimeWiringOptions{FilesystemContext: runtimeWireFilesystemContext(t, root), Client: client},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if err := wiring.LocalTools.Registry().ReplaceHandlers(tools.HandlerRegistration{
		ID:      toolspec.ToolPatch,
		Handler: mismatchedDeletionPresentationHandler{},
	}); err != nil {
		t.Fatalf("ReplaceHandlers: %v", err)
	}

	_, _ = wiring.Engine.SubmitUserMessage(context.Background(), "delete target")
}

var runtimeWireTestSessionPersistence = sessiontest.NewPersistence()

func runtimeWireFilesystemContext(t *testing.T, root string) tools.FilesystemContext {
	t.Helper()
	context, err := NewFilesystemContext(root, root, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	return context
}

func TestBuildToolRegistryAllowsHostedWebSearchWithoutLocalRuntimeBuilder(t *testing.T) {
	workspace := t.TempDir()

	registry, _ := newRuntimeWireToolRegistry(t, workspace, toolspec.ToolExecCommand, toolspec.ToolWebSearch)

	defs := registry.Definitions()
	if len(defs) != 1 {
		t.Fatalf("expected only local runtime tools in registry, got %d", len(defs))
	}
	if defs[0].ID != toolspec.ToolExecCommand {
		t.Fatalf("expected exec_command runtime tool definition, got %+v", defs[0])
	}
}

func TestRuntimeWirePreparesExactlyEightOrdinaryStaticContracts(t *testing.T) {
	sources := staticToolContractSources()
	if len(sources) != 8 {
		t.Fatalf("static contract source count = %d, want 8", len(sources))
	}
	contracts, err := tools.NewStaticToolContracts(jsoncontract.NewPreparer(false), sources...)
	if err != nil {
		t.Fatalf("prepare static contracts: %v", err)
	}
	registrations := make([]tools.HandlerRegistration, 0, len(sources))
	for _, source := range sources {
		if source.ID == toolspec.ToolCompleteNode {
			t.Fatal("complete_node entered ordinary static contract sources")
		}
		registrations = append(registrations, tools.HandlerRegistration{
			ID:      source.ID,
			Handler: mismatchedDeletionPresentationHandler{},
		})
	}
	registry, err := tools.NewStaticToolRegistry(contracts, registrations...)
	if err != nil {
		t.Fatalf("create static registry: %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 8 {
		t.Fatalf("static definition count = %d, want 8", len(definitions))
	}
	for _, definition := range definitions {
		var schema map[string]any
		if err := json.Unmarshal(definition.Schema.JSON(), &schema); err != nil {
			t.Fatalf("decode %s schema: %v", definition.ID, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema is not closed: %#v", definition.ID, schema)
		}
		if _, ok := schema["$defs"]; ok {
			t.Fatalf("%s schema contains provider-incompatible references", definition.ID)
		}
	}
}

func TestRegistryPrepareInputPreservesAliasesAndCanonicalPrecedence(t *testing.T) {
	registry, _ := newRuntimeWireToolRegistry(
		t,
		t.TempDir(),
		toolspec.ToolExecCommand,
		toolspec.ToolEdit,
		toolspec.ToolAskQuestion,
	)
	tests := []struct {
		name    string
		id      toolspec.ID
		raw     string
		want    map[string]any
		wantErr bool
	}{
		{
			name: "canonical wins over wrong typed alias",
			id:   toolspec.ToolExecCommand,
			raw:  `{"cmd":"canonical","command":7}`,
			want: map[string]any{"cmd": "canonical"},
		},
		{
			name: "alias only",
			id:   toolspec.ToolExecCommand,
			raw:  `{"command":"alias"}`,
			want: map[string]any{"cmd": "alias"},
		},
		{
			name: "edit aliases and unknown field",
			id:   toolspec.ToolEdit,
			raw:  `{"filePath":"a.go","oldText":"old","newText":"new","replaceAll":true,"unknown":1}`,
			want: map[string]any{
				"path":        "a.go",
				"old_string":  "old",
				"new_string":  "new",
				"replace_all": true,
			},
		},
		{
			name: "edit canonical wins over aliases",
			id:   toolspec.ToolEdit,
			raw:  `{"path":"a.go","filePath":7,"old_string":"old","oldText":null,"new_string":"new","newText":[]}`,
			want: map[string]any{"path": "a.go", "old_string": "old", "new_string": "new"},
		},
		{
			name: "legacy ask fields are ignored",
			id:   toolspec.ToolAskQuestion,
			raw:  `{"question":"Continue?","action":"legacy","approval":true,"approval_options":[]}`,
			want: map[string]any{"question": "Continue?"},
		},
		{
			name:    "wrong canonical type",
			id:      toolspec.ToolEdit,
			raw:     `{"path":7,"old_string":"old","new_string":"new"}`,
			wantErr: true,
		},
		{
			name:    "null optional array",
			id:      toolspec.ToolAskQuestion,
			raw:     `{"question":"Continue?","suggestions":null}`,
			wantErr: true,
		},
		{
			name:    "missing required",
			id:      toolspec.ToolExecCommand,
			raw:     `{"workdir":"."}`,
			wantErr: true,
		},
		{
			name:    "input must be object",
			id:      toolspec.ToolExecCommand,
			raw:     `null`,
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := registry.PrepareInput(test.id, json.RawMessage(test.raw))
			if test.wantErr {
				if err == nil {
					t.Fatalf("PrepareInput(%s) unexpectedly succeeded: %s", test.id, prepared)
				}
				return
			}
			if err != nil {
				t.Fatalf("PrepareInput(%s): %v", test.id, err)
			}
			var got map[string]any
			if err := json.Unmarshal(prepared, &got); err != nil {
				t.Fatalf("decode prepared input: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("prepared input = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestLocalToolRegistrySiblingWorkspaceBypassesNativeToolApprovals(t *testing.T) {
	workspace := t.TempDir()
	sibling := t.TempDir()
	editPath := filepath.Join(sibling, "edit.txt")
	if err := os.WriteFile(editPath, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write edit fixture: %v", err)
	}
	imagePath := filepath.Join(sibling, "image.pdf")
	if err := os.WriteFile(imagePath, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	filesystemContext, err := NewFilesystemContext(workspace, workspace, metadata.ProjectWorkspaceBoundary{
		ProjectID:  "project",
		Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: sibling}},
	})
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	binding, broker, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   filesystemContext,
		Enabled:             []toolspec.ID{toolspec.ToolEdit, toolspec.ToolPatch, toolspec.ToolViewImage},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("NewLocalToolRegistryBinding: %v", err)
	}
	var approvalRequests int
	broker.SetAskHandler(func(_ context.Context, _ askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		approvalRequests++
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionDeny}, nil
	})

	editHandler, ok := binding.Registry().Get(toolspec.ToolEdit)
	if !ok {
		t.Fatal("missing edit handler")
	}
	editInput, err := json.Marshal(map[string]any{
		"path":       editPath,
		"old_string": "before",
		"new_string": "after",
	})
	if err != nil {
		t.Fatalf("marshal edit input: %v", err)
	}
	editResult, err := editHandler.Call(context.Background(), tools.Call{ID: "sibling-edit", Name: toolspec.ToolEdit, Input: editInput})
	if err != nil || editResult.IsError {
		t.Fatalf("sibling edit result = %+v, error=%v", editResult, err)
	}

	patchHandler, ok := binding.Registry().Get(toolspec.ToolPatch)
	if !ok {
		t.Fatal("missing patch handler")
	}
	patchInput, err := json.Marshal(map[string]any{
		"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(sibling, "patch.txt") + "\n+patched\n*** End Patch\n",
	})
	if err != nil {
		t.Fatalf("marshal patch input: %v", err)
	}
	patchResult, err := patchHandler.Call(context.Background(), tools.Call{ID: "sibling-patch", Name: toolspec.ToolPatch, Input: patchInput})
	if err != nil || patchResult.IsError {
		t.Fatalf("sibling patch result = %+v, error=%v", patchResult, err)
	}

	viewImageHandler, ok := binding.Registry().Get(toolspec.ToolViewImage)
	if !ok {
		t.Fatal("missing view_image handler")
	}
	viewInput, err := json.Marshal(map[string]any{"path": imagePath})
	if err != nil {
		t.Fatalf("marshal view_image input: %v", err)
	}
	viewResult, err := viewImageHandler.Call(context.Background(), tools.Call{ID: "sibling-view-image", Name: toolspec.ToolViewImage, Input: viewInput})
	if err != nil || viewResult.IsError {
		t.Fatalf("sibling view_image result = %+v, error=%v", viewResult, err)
	}
	if approvalRequests != 0 {
		t.Fatalf("sibling Workspace triggered %d approval requests", approvalRequests)
	}
}

func TestLocalToolRegistryTemporaryPathsBypassNativeToolApprovals(t *testing.T) {
	workspace := t.TempDir()
	temporaryRoot := t.TempDir()
	editPath := filepath.Join(temporaryRoot, "edit.txt")
	if err := os.WriteFile(editPath, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write temporary edit fixture: %v", err)
	}
	imagePath := filepath.Join(temporaryRoot, "image.pdf")
	if err := os.WriteFile(imagePath, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write temporary image fixture: %v", err)
	}
	filesystemContext, err := NewFilesystemContext(workspace, workspace, metadata.ProjectWorkspaceBoundary{
		ProjectID: "project",
	})
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	binding, broker, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   filesystemContext,
		Enabled:             []toolspec.ID{toolspec.ToolEdit, toolspec.ToolPatch, toolspec.ToolViewImage},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("NewLocalToolRegistryBinding: %v", err)
	}
	var approvalRequests int
	broker.SetAskHandler(func(_ context.Context, _ askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		approvalRequests++
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionDeny}, nil
	})

	editHandler, ok := binding.Registry().Get(toolspec.ToolEdit)
	if !ok {
		t.Fatal("missing edit handler")
	}
	editInput, err := json.Marshal(map[string]any{
		"path":       editPath,
		"old_string": "before",
		"new_string": "after",
	})
	if err != nil {
		t.Fatalf("marshal edit input: %v", err)
	}
	editResult, err := editHandler.Call(context.Background(), tools.Call{ID: "temporary-edit", Name: toolspec.ToolEdit, Input: editInput})
	if err != nil || editResult.IsError {
		t.Fatalf("temporary edit result = %+v, error=%v", editResult, err)
	}

	patchHandler, ok := binding.Registry().Get(toolspec.ToolPatch)
	if !ok {
		t.Fatal("missing patch handler")
	}
	patchInput, err := json.Marshal(map[string]any{
		"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(temporaryRoot, "patch.txt") + "\n+patched\n*** End Patch\n",
	})
	if err != nil {
		t.Fatalf("marshal patch input: %v", err)
	}
	patchResult, err := patchHandler.Call(context.Background(), tools.Call{ID: "temporary-patch", Name: toolspec.ToolPatch, Input: patchInput})
	if err != nil || patchResult.IsError {
		t.Fatalf("temporary patch result = %+v, error=%v", patchResult, err)
	}

	viewImageHandler, ok := binding.Registry().Get(toolspec.ToolViewImage)
	if !ok {
		t.Fatal("missing view_image handler")
	}
	viewInput, err := json.Marshal(map[string]any{"path": imagePath})
	if err != nil {
		t.Fatalf("marshal view_image input: %v", err)
	}
	viewResult, err := viewImageHandler.Call(context.Background(), tools.Call{ID: "temporary-view-image", Name: toolspec.ToolViewImage, Input: viewInput})
	if err != nil || viewResult.IsError {
		t.Fatalf("temporary view_image result = %+v, error=%v", viewResult, err)
	}
	if approvalRequests != 0 {
		t.Fatalf("temporary paths triggered %d approval requests", approvalRequests)
	}
}

func TestPromptFacingSnapshotReloaderUsesActiveWorkspaceRoot(t *testing.T) {
	configRoot := t.TempDir()
	originalWorkspace := t.TempDir()
	activeWorkspace := t.TempDir()
	for _, workspace := range []string{originalWorkspace, activeWorkspace} {
		configDir := filepath.Join(workspace, config.ConfigDirName)
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir workspace config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("system_prompt_file = \"system.md\"\n"), 0o644); err != nil {
			t.Fatalf("write workspace config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "system.md"), []byte(filepath.Base(workspace)), 0o644); err != nil {
			t.Fatalf("write system prompt: %v", err)
		}
	}
	store, err := session.Create(t.TempDir(), "ws", originalWorkspace, sessioncontract.SessionCategoryMain, runtimeWireTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	reloader := launchPromptFacingSnapshotReloader{
		store:         store,
		workspaceRoot: activeWorkspace,
		configRoot:    configRoot,
	}

	reloaded, err := reloader.ReloadPromptFacingSnapshotConfig(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("reload prompt-facing config: %v", err)
	}
	if len(reloaded.Settings.SystemPromptFiles) == 0 {
		t.Fatal("expected system prompt files from active workspace config")
	}
	got := reloaded.Settings.SystemPromptFiles[len(reloaded.Settings.SystemPromptFiles)-1].Path
	want := filepath.Join(activeWorkspace, config.ConfigDirName, "system.md")
	if got != want {
		t.Fatalf("system prompt path = %q, want active workspace path %q", got, want)
	}
}

func TestOutsideWorkspaceToolsInheritTypedApprovalBarrierFromCallContext(t *testing.T) {
	workspace := t.TempDir()
	outside := outsideNonTempDir(t)
	patchPath := filepath.Join(outside, "patch.txt")
	editPath := filepath.Join(outside, "edit.txt")
	imagePath := filepath.Join(outside, "image.png")
	for path, contents := range map[string][]byte{
		patchPath: []byte("before\n"),
		editPath:  []byte("before\n"),
		imagePath: []byte("not-read-after-barrier"),
	} {
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatalf("write outside fixture %s: %v", path, err)
		}
	}
	registry, broker := newRuntimeWireToolRegistry(
		t,
		workspace,
		toolspec.ToolPatch,
		toolspec.ToolEdit,
		toolspec.ToolViewImage,
	)
	handlerCalled := false
	broker.SetAskHandler(func(_ context.Context, _ askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		handlerCalled = true
		return askquestion.AskQuestionApproval{
			Decision: askquestion.AskQuestionApprovalDecisionAllowOnce,
		}, nil
	})
	barrierErr := errors.New("Approval durability barrier failed")
	encode := func(value any) json.RawMessage {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal outside-workspace tool input: %v", err)
		}
		return encoded
	}

	tests := []struct {
		name          string
		toolID        toolspec.ID
		input         json.RawMessage
		unchangedPath string
		wantContents  []byte
	}{
		{
			name:   "patch",
			toolID: toolspec.ToolPatch,
			input: encode(map[string]any{
				"patch": "*** Begin Patch\n*** Update File: " + patchPath + "\n-before\n+after\n*** End Patch\n",
			}),
			unchangedPath: patchPath,
			wantContents:  []byte("before\n"),
		},
		{
			name:   "edit",
			toolID: toolspec.ToolEdit,
			input: encode(map[string]any{
				"path":       editPath,
				"old_string": "before",
				"new_string": "after",
			}),
			unchangedPath: editPath,
			wantContents:  []byte("before\n"),
		},
		{
			name:   "view_image",
			toolID: toolspec.ToolViewImage,
			input: encode(map[string]any{
				"path": imagePath,
			}),
			unchangedPath: imagePath,
			wantContents:  []byte("not-read-after-barrier"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			barrierCalls := 0
			ctx := tools.WithEffectBarrier(
				context.Background(),
				func(reason tools.EffectBarrierReason) error {
					barrierCalls++
					if reason != tools.EffectBarrierApproval {
						t.Fatalf("barrier reason = %d, want Approval", reason)
					}
					return barrierErr
				},
			)
			handler, ok := registry.Get(test.toolID)
			if !ok {
				t.Fatalf("missing %s handler", test.toolID)
			}

			result, err := handler.Call(ctx, tools.Call{
				ID:    "call-" + test.name,
				Name:  test.toolID,
				Input: test.input,
			})
			if err != nil {
				t.Fatalf("%s call returned operational error: %v", test.name, err)
			}
			if !result.IsError {
				t.Fatalf("%s result = %+v, want provisional approval error", test.name, result)
			}
			if barrierCalls != 1 {
				t.Fatalf("%s barrier calls = %d, want one", test.name, barrierCalls)
			}
			got, err := os.ReadFile(test.unchangedPath)
			if err != nil {
				t.Fatalf("read %s fixture: %v", test.name, err)
			}
			if string(got) != string(test.wantContents) {
				t.Fatalf("%s changed guarded file to %q", test.name, got)
			}
		})
	}
	if handlerCalled {
		t.Fatal("outside-workspace Approval entered broker handler after barrier failure")
	}
}

func TestRuntimewireGeneratedWritePolicyUsesActivePersistenceRoot(t *testing.T) {
	workspace := t.TempDir()
	configRoot := t.TempDir()
	generatedRoot := filepath.Join(configRoot, ".generated")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		t.Fatalf("mkdir generated root: %v", err)
	}
	registry, _ := newRuntimeWireToolRegistryWithConfig(t, workspace, configRoot, false, toolspec.ToolPatch, toolspec.ToolEdit)

	patchHandler, ok := registry.Get(toolspec.ToolPatch)
	if !ok {
		t.Fatal("expected patch handler")
	}
	patchInput, _ := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(generatedRoot, "skill.txt") + "\n+generated\n*** End Patch\n"})
	patchResult, err := patchHandler.Call(context.Background(), tools.Call{ID: "patch-generated", Name: toolspec.ToolPatch, Input: patchInput})
	if err != nil {
		t.Fatalf("patch call: %v", err)
	}
	if !patchResult.IsError || !strings.Contains(string(patchResult.Output), filepath.Join(configRoot, "skills")+string(filepath.Separator)) {
		t.Fatalf("expected generated patch denial with active skills path, got error=%t output=%s", patchResult.IsError, string(patchResult.Output))
	}
	if _, err := os.Stat(filepath.Join(generatedRoot, "skill.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated patch created file, stat err=%v", err)
	}
	missingAncestorInput, _ := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(generatedRoot, "missing", "deep.txt") + "\n+generated\n*** End Patch\n"})
	missingAncestorResult, err := patchHandler.Call(context.Background(), tools.Call{ID: "patch-generated-missing-ancestor", Name: toolspec.ToolPatch, Input: missingAncestorInput})
	if err != nil {
		t.Fatalf("patch missing ancestor call: %v", err)
	}
	if !missingAncestorResult.IsError || !strings.Contains(string(missingAncestorResult.Output), filepath.Join(configRoot, "skills")+string(filepath.Separator)) {
		t.Fatalf("expected generated missing-ancestor denial, got error=%t output=%s", missingAncestorResult.IsError, string(missingAncestorResult.Output))
	}

	editHandler, ok := registry.Get(toolspec.ToolEdit)
	if !ok {
		t.Fatal("expected edit handler")
	}
	editInput, _ := json.Marshal(map[string]any{"path": filepath.Join(generatedRoot, "edit.txt"), "old_string": "", "new_string": "generated\n"})
	editResult, err := editHandler.Call(context.Background(), tools.Call{ID: "edit-generated", Name: toolspec.ToolEdit, Input: editInput})
	if err != nil {
		t.Fatalf("edit call: %v", err)
	}
	if !editResult.IsError || !strings.Contains(string(editResult.Output), filepath.Join(configRoot, "skills")+string(filepath.Separator)) {
		t.Fatalf("expected generated edit denial with active skills path, got error=%t output=%s", editResult.IsError, string(editResult.Output))
	}
	rootEditInput, _ := json.Marshal(map[string]any{"path": generatedRoot, "old_string": "old", "new_string": "new"})
	rootEditResult, err := editHandler.Call(context.Background(), tools.Call{ID: "edit-generated-root", Name: toolspec.ToolEdit, Input: rootEditInput})
	if err != nil {
		t.Fatalf("edit root call: %v", err)
	}
	if !rootEditResult.IsError || !strings.Contains(string(rootEditResult.Output), filepath.Join(configRoot, "skills")+string(filepath.Separator)) {
		t.Fatalf("expected generated root denial, got error=%t output=%s", rootEditResult.IsError, string(rootEditResult.Output))
	}
}

func TestRuntimewireGeneratedWritePolicyDefaultGuidanceAndSiblingFallthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	defaultRoot := filepath.Join(home, config.ConfigDirName)
	generatedRoot := filepath.Join(defaultRoot, ".generated")
	siblingRoot := filepath.Join(defaultRoot, ".generated-backup")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		t.Fatalf("mkdir generated root: %v", err)
	}
	if err := os.MkdirAll(siblingRoot, 0o755); err != nil {
		t.Fatalf("mkdir sibling root: %v", err)
	}
	registry, _ := newRuntimeWireToolRegistryWithConfig(t, workspace, "", true, toolspec.ToolPatch)
	patchHandler, ok := registry.Get(toolspec.ToolPatch)
	if !ok {
		t.Fatal("expected patch handler")
	}

	deniedInput, _ := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(generatedRoot, "root-denied.txt") + "\n+generated\n*** End Patch\n"})
	denied, err := patchHandler.Call(context.Background(), tools.Call{ID: "patch-default-generated", Name: toolspec.ToolPatch, Input: deniedInput})
	if err != nil {
		t.Fatalf("patch generated call: %v", err)
	}
	if !denied.IsError || !strings.Contains(string(denied.Output), "~/.kent/skills/") {
		t.Fatalf("expected default generated guidance, got error=%t output=%s", denied.IsError, string(denied.Output))
	}

	siblingTarget := filepath.Join(siblingRoot, "allowed.txt")
	allowedInput, _ := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Add File: " + siblingTarget + "\n+allowed\n*** End Patch\n"})
	allowed, err := patchHandler.Call(context.Background(), tools.Call{ID: "patch-generated-sibling", Name: toolspec.ToolPatch, Input: allowedInput})
	if err != nil {
		t.Fatalf("patch sibling call: %v", err)
	}
	if allowed.IsError {
		t.Fatalf("expected sibling path to follow configured outside-workspace allow, got %s", string(allowed.Output))
	}
	if data, err := os.ReadFile(siblingTarget); err != nil || string(data) != "allowed\n" {
		t.Fatalf("sibling target content = %q err=%v", string(data), err)
	}
}

func TestRuntimewireGeneratedPolicyPreservedAcrossWorkspaceRebind(t *testing.T) {
	configRoot := t.TempDir()
	generatedRoot := filepath.Join(configRoot, ".generated")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		t.Fatalf("mkdir generated root: %v", err)
	}
	binding, _, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   runtimewirefixture.FilesystemContext(t, t.TempDir()),
		Enabled:             []toolspec.ID{toolspec.ToolPatch},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
		GlobalConfigDir:     configRoot,
	})
	if err != nil {
		t.Fatalf("new local tool registry binding: %v", err)
	}
	if err := binding.ReplaceFilesystemContext(runtimewirefixture.FilesystemContext(t, t.TempDir())); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	patchHandler, ok := binding.Registry().Get(toolspec.ToolPatch)
	if !ok {
		t.Fatal("expected patch handler")
	}
	input, _ := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(generatedRoot, "after-rebind.txt") + "\n+generated\n*** End Patch\n"})
	result, err := patchHandler.Call(context.Background(), tools.Call{ID: "patch-after-rebind", Name: toolspec.ToolPatch, Input: input})
	if err != nil {
		t.Fatalf("patch call: %v", err)
	}
	if !result.IsError || !strings.Contains(string(result.Output), filepath.Join(configRoot, "skills")+string(filepath.Separator)) {
		t.Fatalf("expected generated denial after rebind, got error=%t output=%s", result.IsError, string(result.Output))
	}
}

func TestRuntimewireViewImageReadsGeneratedFileWithNormalApproval(t *testing.T) {
	workspace := t.TempDir()
	configRoot := t.TempDir()
	generatedRoot := filepath.Join(configRoot, ".generated")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		t.Fatalf("mkdir generated root: %v", err)
	}
	generatedPDF := filepath.Join(generatedRoot, "doc.pdf")
	if err := os.WriteFile(generatedPDF, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write generated pdf: %v", err)
	}
	registry, broker := newRuntimeWireToolRegistryWithConfig(t, workspace, configRoot, false, toolspec.ToolPatch, toolspec.ToolViewImage)
	broker.SetAskHandler(func(_ context.Context, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		if !req.Approval || len(req.ApprovalOptions) != 3 {
			t.Fatalf("generated-file request = %+v, want structured approval", req)
		}
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce}, nil
	})

	viewImageHandler, ok := registry.Get(toolspec.ToolViewImage)
	if !ok {
		t.Fatal("expected view_image handler")
	}
	viewInput, _ := json.Marshal(map[string]any{"path": generatedPDF})
	viewResult, err := viewImageHandler.Call(context.Background(), tools.Call{ID: "view-generated", Name: toolspec.ToolViewImage, Input: viewInput})
	if err != nil {
		t.Fatalf("view_image call: %v", err)
	}
	if viewResult.IsError {
		t.Fatalf("expected generated read success after normal approval, got %s", string(viewResult.Output))
	}

	patchHandler, ok := registry.Get(toolspec.ToolPatch)
	if !ok {
		t.Fatal("expected patch handler")
	}
	patchInput, _ := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Add File: " + filepath.Join(generatedRoot, "write.txt") + "\n+generated\n*** End Patch\n"})
	patchResult, err := patchHandler.Call(context.Background(), tools.Call{ID: "patch-generated-read-regression", Name: toolspec.ToolPatch, Input: patchInput})
	if err != nil {
		t.Fatalf("patch call: %v", err)
	}
	if !patchResult.IsError {
		t.Fatal("expected generated write denial while read remains allowed")
	}
}

func TestLocalToolRegistryBindingRebindUpdatesExecCommandRoot(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "workspace-a")
	rootB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatalf("mkdir rootA: %v", err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatalf("mkdir rootB: %v", err)
	}
	binding := newRuntimeWireBinding(t, rootA, toolspec.ToolExecCommand)
	if got := shellPwdOutput(t, binding.Registry()); got != canonicalPathForTest(t, rootA) {
		t.Fatalf("pwd before rebind = %q, want %q", got, canonicalPathForTest(t, rootA))
	}
	if err := binding.ReplaceFilesystemContext(runtimewirefixture.FilesystemContext(t, rootB)); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got := shellPwdOutput(t, binding.Registry()); got != canonicalPathForTest(t, rootB) {
		t.Fatalf("pwd after rebind = %q, want %q", got, canonicalPathForTest(t, rootB))
	}
}

func TestReplaceFilesystemContextReplacesNativeToolTrustAndProjectWorkspaces(t *testing.T) {
	rootA := outsideNonTempDir(t)
	rootB := outsideNonTempDir(t)
	projectRootA := outsideNonTempDir(t)
	projectRootB := outsideNonTempDir(t)
	writeText := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
			t.Fatalf("write text fixture %s: %v", path, err)
		}
	}
	writePDF := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o644); err != nil {
			t.Fatalf("write PDF fixture %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(rootA, "patch.txt"),
		filepath.Join(rootA, "edit.txt"),
		filepath.Join(rootB, "patch.txt"),
		filepath.Join(rootB, "edit.txt"),
		filepath.Join(projectRootA, "patch.txt"),
		filepath.Join(projectRootB, "patch.txt"),
	} {
		writeText(path)
	}
	writePDF(filepath.Join(rootA, "image.pdf"))
	writePDF(filepath.Join(rootB, "image.pdf"))

	initial, err := NewFilesystemContext(rootA, rootA, metadata.ProjectWorkspaceBoundary{
		ProjectID:  "project",
		Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: projectRootA}},
	})
	if err != nil {
		t.Fatalf("initial filesystem context: %v", err)
	}
	binding, broker, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   initial,
		Enabled:             []toolspec.ID{toolspec.ToolPatch, toolspec.ToolEdit, toolspec.ToolViewImage},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("new local tool registry binding: %v", err)
	}
	approvalRequests := 0
	broker.SetAskHandler(func(_ context.Context, _ askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		approvalRequests++
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionDeny}, nil
	})

	next, err := NewFilesystemContext(rootB, rootB, metadata.ProjectWorkspaceBoundary{
		ProjectID:  "project",
		Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: projectRootB}},
	})
	if err != nil {
		t.Fatalf("replacement filesystem context: %v", err)
	}
	if err := binding.ReplaceFilesystemContext(next); err != nil {
		t.Fatalf("ReplaceFilesystemContext: %v", err)
	}

	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolPatch, map[string]any{
		"patch": "*** Begin Patch\n*** Update File: " + filepath.Join(rootB, "patch.txt") + "\n-before\n+after\n*** End Patch\n",
	})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolEdit, map[string]any{
		"path":       filepath.Join(rootB, "edit.txt"),
		"old_string": "before",
		"new_string": "after",
	})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolViewImage, map[string]any{
		"path": filepath.Join(rootB, "image.pdf"),
	})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolPatch, map[string]any{
		"patch": "*** Begin Patch\n*** Update File: " + filepath.Join(projectRootB, "patch.txt") + "\n-before\n+after\n*** End Patch\n",
	})
	if approvalRequests != 0 {
		t.Fatalf("replacement roots triggered %d approval requests", approvalRequests)
	}

	assertRuntimeWireToolError(t, binding.Registry(), toolspec.ToolPatch, map[string]any{
		"patch": "*** Begin Patch\n*** Update File: " + filepath.Join(rootA, "patch.txt") + "\n-before\n+after\n*** End Patch\n",
	})
	assertRuntimeWireToolError(t, binding.Registry(), toolspec.ToolEdit, map[string]any{
		"path":       filepath.Join(rootA, "edit.txt"),
		"old_string": "before",
		"new_string": "after",
	})
	assertRuntimeWireToolError(t, binding.Registry(), toolspec.ToolViewImage, map[string]any{
		"path": filepath.Join(rootA, "image.pdf"),
	})
	assertRuntimeWireToolError(t, binding.Registry(), toolspec.ToolPatch, map[string]any{
		"patch": "*** Begin Patch\n*** Update File: " + filepath.Join(projectRootA, "patch.txt") + "\n-before\n+after\n*** End Patch\n",
	})
	if approvalRequests != 4 {
		t.Fatalf("retired roots triggered %d approval requests, want 4", approvalRequests)
	}
}

func TestReplaceFilesystemContextReplacesMutationManagedWorktreePolicyWithoutRestrictingReads(t *testing.T) {
	base := outsideNonTempDir(t)
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	for _, root := range []string{currentRoot, foreignRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir managed worktree root %s: %v", root, err)
		}
	}
	foreignPatch := filepath.Join(foreignRoot, "patch.txt")
	foreignEdit := filepath.Join(foreignRoot, "edit.txt")
	foreignImage := filepath.Join(foreignRoot, "image.pdf")
	for _, path := range []string{foreignPatch, foreignEdit} {
		if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
			t.Fatalf("write managed worktree fixture %s: %v", path, err)
		}
	}
	if err := os.WriteFile(foreignImage, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write managed worktree PDF: %v", err)
	}
	managed, err := tools.NewManagedWorktreePathContext(base, &currentRoot, []string{currentRoot, foreignRoot})
	if err != nil {
		t.Fatalf("NewManagedWorktreePathContext: %v", err)
	}
	initial := runtimewirefixture.FilesystemContext(t, currentRoot)
	binding, _, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   initial,
		Enabled:             []toolspec.ID{toolspec.ToolPatch, toolspec.ToolEdit, toolspec.ToolViewImage},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		AllowNonCwdEdits:    true,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("new local tool registry binding: %v", err)
	}
	next := initial.Clone()
	next.ManagedWorktree = managed
	if err := binding.ReplaceFilesystemContext(next); err != nil {
		t.Fatalf("ReplaceFilesystemContext: %v", err)
	}

	assertRuntimeWireToolError(t, binding.Registry(), toolspec.ToolPatch, map[string]any{
		"patch": "*** Begin Patch\n*** Update File: " + foreignPatch + "\n-before\n+after\n*** End Patch\n",
	})
	assertRuntimeWireToolError(t, binding.Registry(), toolspec.ToolEdit, map[string]any{
		"path":       foreignEdit,
		"old_string": "before",
		"new_string": "after",
	})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolViewImage, map[string]any{
		"path": foreignImage,
	})
}

func TestReplaceFilesystemContextPreservesSessionApprovalsAcrossRebuildAndRejectedReplacement(t *testing.T) {
	rootA := outsideNonTempDir(t)
	rootB := outsideNonTempDir(t)
	outside := outsideNonTempDir(t)
	patchBefore := filepath.Join(outside, "patch-before.txt")
	editAfter := filepath.Join(rootA, "edit-after.txt")
	imageBefore := filepath.Join(outside, "image-before.pdf")
	imageAfter := filepath.Join(rootA, "image-after.pdf")
	for _, path := range []string{patchBefore, editAfter} {
		if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
			t.Fatalf("write edit fixture %s: %v", path, err)
		}
	}
	for _, path := range []string{imageBefore, imageAfter} {
		if err := os.WriteFile(path, []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o644); err != nil {
			t.Fatalf("write image fixture %s: %v", path, err)
		}
	}
	binding, broker, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   runtimewirefixture.FilesystemContext(t, rootA),
		Enabled:             []toolspec.ID{toolspec.ToolPatch, toolspec.ToolEdit, toolspec.ToolViewImage},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("new local tool registry binding: %v", err)
	}
	approvalRequests := 0
	broker.SetAskHandler(func(_ context.Context, _ askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		approvalRequests++
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionAllowSession}, nil
	})

	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolPatch, map[string]any{
		"patch": "*** Begin Patch\n*** Update File: " + patchBefore + "\n-before\n+after\n*** End Patch\n",
	})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolViewImage, map[string]any{"path": imageBefore})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolViewImage, map[string]any{"path": imageBefore})
	if approvalRequests != 2 {
		t.Fatalf("fresh edit/read approval requests = %d, want 2", approvalRequests)
	}

	if err := binding.ReplaceFilesystemContext(runtimewirefixture.FilesystemContext(t, rootB)); err != nil {
		t.Fatalf("ReplaceFilesystemContext: %v", err)
	}
	contextBeforeFailure := binding.FilesystemContext()
	if err := binding.ReplaceFilesystemContext(tools.FilesystemContext{}); err == nil {
		t.Fatal("ReplaceFilesystemContext accepted an invalid context")
	}
	if !binding.FilesystemContext().Equal(contextBeforeFailure) {
		t.Fatal("failed filesystem context replacement changed the active context")
	}

	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolEdit, map[string]any{
		"path":       editAfter,
		"old_string": "before",
		"new_string": "after",
	})
	assertRuntimeWireToolSuccess(t, binding.Registry(), toolspec.ToolViewImage, map[string]any{"path": imageAfter})
	if approvalRequests != 2 {
		t.Fatalf("approval requests after rebuild = %d, want cached edit/read decisions", approvalRequests)
	}
}

func assertRuntimeWireToolSuccess(t *testing.T, registry *tools.Registry, id toolspec.ID, input any) {
	t.Helper()
	result := callRuntimeWireTool(t, registry, id, input)
	if result.IsError {
		t.Fatalf("%s result = error: %s", id, string(result.Output))
	}
}

func assertRuntimeWireToolError(t *testing.T, registry *tools.Registry, id toolspec.ID, input any) {
	t.Helper()
	result := callRuntimeWireTool(t, registry, id, input)
	if !result.IsError {
		t.Fatalf("%s result = success, want policy rejection", id)
	}
}

func callRuntimeWireTool(t *testing.T, registry *tools.Registry, id toolspec.ID, input any) tools.Result {
	t.Helper()
	handler, ok := registry.Get(id)
	if !ok {
		t.Fatalf("missing %s handler", id)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal %s input: %v", id, err)
	}
	result, err := handler.Call(context.Background(), tools.Call{
		ID:    "runtimewire-" + string(id),
		Name:  id,
		Input: encoded,
	})
	if err != nil {
		t.Fatalf("%s call: %v", id, err)
	}
	return result
}

func TestLocalToolRegistryBindingBindsExecutionCorrelationPerSuccessiveScope(t *testing.T) {
	workspace := t.TempDir()
	manager, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(50*time.Millisecond),
		shelltool.WithPostprocessor(runtimeWirePostprocessor(t, config.ShellPostprocessingModeBuiltin, nil)),
	)
	if err != nil {
		t.Fatalf("new shell manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	binding, _, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   runtimewirefixture.FilesystemContext(t, workspace),
		Enabled:             []toolspec.ID{toolspec.ToolExecCommand},
		MinimumExecToBgTime: 50 * time.Millisecond,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
		Background:          manager,
	})
	if err != nil {
		t.Fatalf("new local tool registry binding: %v", err)
	}
	events := make(chan shelltool.Event, 6)
	manager.SetEventHandler(func(event shelltool.Event) bool {
		events <- event
		return true
	})
	nextEvent := func() shelltool.Event {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for shell event")
			return shelltool.Event{}
		}
	}
	startBackground := func(callID string) shelltool.Snapshot {
		t.Helper()
		handler, ok := binding.Registry().Get(toolspec.ToolExecCommand)
		if !ok {
			t.Fatal("expected exec_command handler")
		}
		input, err := json.Marshal(map[string]any{
			"cmd":           "sleep 1",
			"shell":         "/bin/sh",
			"login":         false,
			"yield_time_ms": 50,
		})
		if err != nil {
			t.Fatalf("marshal exec_command input: %v", err)
		}
		result, err := handler.Call(context.Background(), tools.Call{
			ID:    callID,
			Name:  toolspec.ToolExecCommand,
			Input: input,
		})
		if err != nil {
			t.Fatalf("exec_command call: %v", err)
		}
		if result.IsError {
			t.Fatalf("exec_command result error: %s", string(result.Output))
		}
		if result.PresentationDelta == nil || !result.PresentationDelta.MovedToBackground {
			t.Fatalf("exec_command result did not move to background: %+v", result.PresentationDelta)
		}
		event := nextEvent()
		if event.Type != shelltool.EventBackgrounded {
			t.Fatalf("event type = %q, want %q", event.Type, shelltool.EventBackgrounded)
		}
		return event.Snapshot
	}
	assertCorrelation := func(location string, got *runtimeids.ExecutionCorrelation, want *runtimeids.ExecutionCorrelation) {
		t.Helper()
		if want == nil {
			if got != nil {
				t.Fatalf("%s execution correlation = %#v, want nil", location, *got)
			}
			return
		}
		if got == nil {
			t.Fatalf("%s execution correlation is nil", location)
		}
		if *got != *want {
			t.Fatalf("%s execution correlation = %#v, want %#v", location, *got, *want)
		}
	}

	correlationA, err := runtimeids.NewExecutionCorrelation(runtimeids.NewExecutionScopeID(), runtimeids.ResourceGeneration(1))
	if err != nil {
		t.Fatalf("new correlation A: %v", err)
	}
	if err := binding.BindExecutionCorrelation(&correlationA); err != nil {
		t.Fatalf("bind correlation A: %v", err)
	}
	snapshotA := startBackground("scope-a")
	assertCorrelation("scope A snapshot", snapshotA.ExecutionCorrelation, &correlationA)

	correlationB, err := runtimeids.NewExecutionCorrelation(runtimeids.NewExecutionScopeID(), runtimeids.ResourceGeneration(1))
	if err != nil {
		t.Fatalf("new correlation B: %v", err)
	}
	if err := binding.BindExecutionCorrelation(&correlationB); err != nil {
		t.Fatalf("bind correlation B: %v", err)
	}
	currentA, err := manager.Snapshot(snapshotA.ID)
	if err != nil {
		t.Fatalf("snapshot scope A after rebind: %v", err)
	}
	assertCorrelation("scope A snapshot after scope B bind", currentA.ExecutionCorrelation, &correlationA)
	snapshotB := startBackground("scope-b")
	assertCorrelation("scope B snapshot", snapshotB.ExecutionCorrelation, &correlationB)

	if err := binding.BindExecutionCorrelation(nil); err != nil {
		t.Fatalf("clear correlation: %v", err)
	}
	snapshotUnscoped := startBackground("scope-idle")
	assertCorrelation("unscoped snapshot", snapshotUnscoped.ExecutionCorrelation, nil)

	expectedByProcessID := map[string]*runtimeids.ExecutionCorrelation{
		snapshotA.ID:        &correlationA,
		snapshotB.ID:        &correlationB,
		snapshotUnscoped.ID: nil,
	}
	for remaining := len(expectedByProcessID); remaining > 0; remaining-- {
		event := nextEvent()
		if event.Type != shelltool.EventCompleted {
			t.Fatalf("terminal event type = %q, want %q", event.Type, shelltool.EventCompleted)
		}
		want, ok := expectedByProcessID[event.Snapshot.ID]
		if !ok {
			t.Fatalf("unexpected terminal process ID %q", event.Snapshot.ID)
		}
		assertCorrelation("terminal event", event.Snapshot.ExecutionCorrelation, want)
		delete(expectedByProcessID, event.Snapshot.ID)
	}
	if len(expectedByProcessID) != 0 {
		t.Fatalf("missing terminal events for %d processes", len(expectedByProcessID))
	}
}

func TestRuntimeWiringExecCommandUsesEffectiveBuiltinInsteadOfBootstrapNone(t *testing.T) {
	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "effective-builtin")
	background := newRuntimeWireShellManager(t, runtimeWirePostprocessor(t, config.ShellPostprocessingModeNone, nil))
	active := runtimeWireShellSettings(config.ShellPostprocessingModeBuiltin, nil)

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		materializedRuntimeWireEventLog(t, store),
		active,
		[]toolspec.ID{toolspec.ToolExecCommand},
		nil,
		nil,
		background,
		RuntimeWiringOptions{FilesystemContext: runtimeWireFilesystemContext(t, root), Client: &runtimewireCaptureClient{}},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if wiring.Background != background {
		t.Fatal("runtime wiring replaced the supplied global shell manager")
	}

	output := callRuntimeWireExec(t, wiring.LocalTools.Registry(), "printf '\\033[31mcolor\\033[0m'")
	if output != "color" {
		t.Fatalf("exec_command output = %q, want builtin output from supplied active settings", output)
	}
	if active.Shell.PostprocessingMode != config.ShellPostprocessingModeBuiltin {
		t.Fatalf("reported active shell mode = %q, want builtin", active.Shell.PostprocessingMode)
	}
}

func TestRuntimeWiringExecCommandUsesEffectiveHookAcrossWorkspaceRebind(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	store := newRuntimeWireSession(t, rootA, "effective-hook")
	bootstrapHook := "BOOTSTRAP"
	effectiveHook := "EFFECTIVE"
	background := newRuntimeWireShellManager(t, runtimeWirePostprocessor(t, config.ShellPostprocessingModeUser, &bootstrapHook))
	effectiveHookPath := runtimeWireHookScript(t, effectiveHook)
	active := runtimeWireShellSettings(config.ShellPostprocessingModeUser, &effectiveHookPath)

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		materializedRuntimeWireEventLog(t, store),
		active,
		[]toolspec.ID{toolspec.ToolExecCommand},
		nil,
		nil,
		background,
		RuntimeWiringOptions{FilesystemContext: runtimeWireFilesystemContext(t, rootA), Client: &runtimewireCaptureClient{}},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if got := callRuntimeWireExec(t, wiring.LocalTools.Registry(), "printf original"); got != effectiveHook {
		t.Fatalf("effective hook output = %q, want %q", got, effectiveHook)
	}
	if err := wiring.LocalTools.ReplaceFilesystemContext(runtimewirefixture.FilesystemContext(t, rootB)); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got := callRuntimeWireExec(t, wiring.LocalTools.Registry(), "printf rebound"); got != effectiveHook {
		t.Fatalf("effective hook output after rebind = %q, want %q", got, effectiveHook)
	}
	if active.Shell.PostprocessHook == nil || *active.Shell.PostprocessHook != effectiveHookPath {
		t.Fatalf("reported active hook = %#v, want %q", active.Shell.PostprocessHook, effectiveHookPath)
	}
}

func runtimeWireShellSettings(mode config.ShellPostprocessingMode, hookPath *string) config.Settings {
	var hook *string
	if hookPath != nil {
		copy := *hookPath
		hook = &copy
	}
	return config.Settings{
		Model:               "gpt-5",
		ModelContextWindow:  200_000,
		Reviewer:            config.ReviewerSettings{Frequency: "off"},
		Timeouts:            config.Timeouts{ModelRequestSeconds: 1},
		ShellOutputMaxChars: 16_000,
		Shell: config.ShellSettings{
			PostprocessingMode: mode,
			PostprocessHook:    hook,
		},
	}
}

func runtimeWirePostprocessor(t *testing.T, mode config.ShellPostprocessingMode, hookReplacement *string) *postprocess.Runner {
	t.Helper()
	var hookPath *string
	if hookReplacement != nil {
		path := runtimeWireHookScript(t, *hookReplacement)
		hookPath = &path
	}
	runner, err := postprocess.NewRunner(postprocess.Settings{Mode: mode, HookPath: hookPath})
	if err != nil {
		t.Fatalf("new postprocess runner: %v", err)
	}
	return runner
}

func runtimeWireHookScript(t *testing.T, replacement string) string {
	t.Helper()
	script := "#!/bin/sh\nprintf '{\"processed\":true,\"replaced_output\":\"" + replacement + "\"}'\n"
	return testsetup.WriteExecutable(t, "hook.sh", script)
}

func newRuntimeWireShellManager(t *testing.T, runner *postprocess.Runner) *shelltool.Manager {
	t.Helper()
	manager, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(250*time.Millisecond),
		shelltool.WithPostprocessor(runner),
	)
	if err != nil {
		t.Fatalf("new shell manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func callRuntimeWireExec(t *testing.T, registry *tools.Registry, command string) string {
	t.Helper()
	handler, ok := registry.Get(toolspec.ToolExecCommand)
	if !ok {
		t.Fatal("expected exec_command handler")
	}
	input, err := json.Marshal(map[string]any{
		"cmd":           command,
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if err != nil {
		t.Fatalf("marshal exec_command input: %v", err)
	}
	result, err := handler.Call(context.Background(), tools.Call{
		ID:    "runtimewire-exec",
		Name:  toolspec.ToolExecCommand,
		Input: input,
	})
	if err != nil {
		t.Fatalf("exec_command call: %v", err)
	}
	if result.IsError {
		t.Fatalf("exec_command result error: %s", string(result.Output))
	}
	var output string
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode exec_command output: %v", err)
	}
	return output
}

func TestNewLocalToolRegistryBindingRejectsEmptyWorkspaceRoot(t *testing.T) {
	_, _, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   tools.FilesystemContext{},
		Enabled:             []toolspec.ID{toolspec.ToolExecCommand},
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if !errors.Is(err, errWorkspaceRootRequired) {
		t.Fatalf("new local tool registry binding error = %v, want errWorkspaceRootRequired", err)
	}
}

func TestNewFilesystemContextValidatesNamedRoots(t *testing.T) {
	t.Parallel()
	executionRoot := t.TempDir()
	workingDirectory := filepath.Join(executionRoot, "nested")
	if err := os.Mkdir(workingDirectory, 0o755); err != nil {
		t.Fatalf("Mkdir working directory: %v", err)
	}
	boundary := metadata.ProjectWorkspaceBoundary{ProjectID: "test", Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: filepath.Join(t.TempDir(), "missing-secondary")}}}
	if context, err := NewFilesystemContext(workingDirectory, executionRoot, boundary); err != nil || len(context.Access.ProjectWorkspace.Roots) != 1 {
		t.Fatalf("optional root = %+v, error=%v", context, err)
	}
	if _, err := NewFilesystemContext(filepath.Join(t.TempDir(), "outside"), executionRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"}); err == nil {
		t.Fatal("accepted working directory outside execution target root")
	}
}

func TestNewFilesystemContextRequiresAvailableMandatoryRootsAndSurfacesSecondaryResolutionErrors(t *testing.T) {
	executionRoot := t.TempDir()
	if _, err := NewFilesystemContext(filepath.Join(executionRoot, "missing"), executionRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing working directory error = %v, want os.ErrNotExist", err)
	}
	if _, err := NewFilesystemContext(executionRoot, filepath.Join(executionRoot, "missing"), metadata.ProjectWorkspaceBoundary{ProjectID: "test"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing execution target error = %v, want os.ErrNotExist", err)
	}

	loop := filepath.Join(executionRoot, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewFilesystemContext(executionRoot, executionRoot, metadata.ProjectWorkspaceBoundary{
		ProjectID:  "test",
		Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: loop}},
	}); err == nil {
		t.Fatal("accepted secondary workspace with an unresolvable symlink")
	}
}

func TestFilesystemContextRejectsOverLimitBoundaryAndReplacement(t *testing.T) {
	executionRoot := t.TempDir()
	workspaces := make([]metadata.ProjectWorkspace, metadata.ProjectWorkspaceCollectionLimit+1)
	for index := range workspaces {
		workspaces[index] = metadata.ProjectWorkspace{
			CanonicalRoot: filepath.Join(t.TempDir(), fmt.Sprintf("workspace-%d", index)),
		}
	}
	_, err := NewFilesystemContext(executionRoot, executionRoot, metadata.ProjectWorkspaceBoundary{
		ProjectID:  "test",
		Workspaces: workspaces,
	})
	if err == nil {
		t.Fatal("NewFilesystemContext accepted an over-limit boundary")
	}

	binding := newRuntimeWireBinding(t, executionRoot, toolspec.ToolExecCommand)
	next := binding.FilesystemContext()
	root := next.Access.WorkingDirectory
	next.Access.ProjectWorkspace.Roots = make([]tools.ProjectWorkspaceRoot, metadata.ProjectWorkspaceCollectionLimit+1)
	for index := range next.Access.ProjectWorkspace.Roots {
		next.Access.ProjectWorkspace.Roots[index] = tools.ProjectWorkspaceRoot{FilesystemRoot: root}
	}
	if err := binding.ReplaceFilesystemContext(next); err == nil {
		t.Fatal("ReplaceFilesystemContext accepted an over-limit materialized scope")
	}

	next = binding.FilesystemContext()
	next.Access.ProjectWorkspace.ProjectID = ""
	if err := binding.ReplaceFilesystemContext(next); err == nil {
		t.Fatal("ReplaceFilesystemContext accepted an empty Project ID")
	}
}

func TestMissingSecondaryWorkspaceIdentityTrustsItsLaterMaterializedRoot(t *testing.T) {
	executionRoot := t.TempDir()
	secondaryParent := t.TempDir()
	missingSecondary := filepath.Join(secondaryParent, "workspace")
	filesystemContext, err := NewFilesystemContext(executionRoot, executionRoot, metadata.ProjectWorkspaceBoundary{
		ProjectID:  "test",
		Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: missingSecondary}},
	})
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	if err := os.MkdirAll(missingSecondary, 0o755); err != nil {
		t.Fatalf("materialize secondary workspace: %v", err)
	}
	target := filepath.Join(missingSecondary, "file.txt")
	if err := os.WriteFile(target, []byte("trusted"), 0o644); err != nil {
		t.Fatalf("write secondary file: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("resolve secondary file: %v", err)
	}
	policy, err := tools.NewFileAccessPolicy(tools.FileAccessPolicyConfig{
		Context: filesystemContext,
		Mode:    tools.FileAccessRead,
	})
	if err != nil {
		t.Fatalf("NewFileAccessPolicy: %v", err)
	}
	outcome := policy.BeginCall().Authorize(context.Background(), target, resolved)
	if !outcome.IsAllowed() || outcome.Reason != tools.FileAccessReasonTrustedRoot {
		t.Fatalf("missing secondary identity authorization = %+v, want trusted root", outcome)
	}
}

func TestLocalToolRegistryBindingRebindRejectsEmptyWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	binding := newRuntimeWireBinding(t, root, toolspec.ToolExecCommand)
	if err := binding.ReplaceFilesystemContext(tools.FilesystemContext{}); !errors.Is(err, errWorkspaceRootRequired) {
		t.Fatalf("context replacement error = %v, want errWorkspaceRootRequired", err)
	}
}

func TestNewRuntimeWiringRejectsEmptyModelAfterBypassingConfigDefaults(t *testing.T) {
	root := t.TempDir()
	store, err := session.Create(root, "ws", root, sessioncontract.SessionCategoryMain, runtimeWireTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	_, err = NewRuntimeWiringWithBackground(
		store,
		materializedRuntimeWireEventLog(t, store),
		config.Settings{
			Model:              "",
			ProviderOverride:   "openai",
			OpenAIBaseURL:      "http://example.test/v1",
			ModelContextWindow: 272_000,
			Timeouts: config.Timeouts{
				ModelRequestSeconds: 1,
			},
			Shell: config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		[]toolspec.ID{toolspec.ToolExecCommand},
		auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, nil),
		nil,
		nil,
		RuntimeWiringOptions{FilesystemContext: runtimeWireFilesystemContext(t, root)},
	)
	if !errors.Is(err, runtime.ErrModelRequired) {
		t.Fatalf("expected runtime.ErrModelRequired, got %v", err)
	}
}

func TestReviewerModelCapabilitiesHonorExplicitFalseSources(t *testing.T) {
	locked := lockedModelCapabilitiesForConfig(
		"gpt-5",
		config.ModelCapabilitiesOverride{SupportsReasoningEffort: false},
		map[string]string{"reviewer.model_capabilities.supports_reasoning_effort": "file"},
		"reviewer.model_capabilities.supports_reasoning_effort",
		"reviewer.model_capabilities.supports_vision_inputs",
	)

	if locked.SupportsReasoningEffort {
		t.Fatalf("expected explicit reviewer reasoning false override to beat model contract, got %+v", locked)
	}
	if !locked.SupportsVisionInputs {
		t.Fatalf("expected default reviewer vision capability to come from model contract, got %+v", locked)
	}
}

func TestReviewerModelCapabilitiesHonorInheritedExplicitFalseSources(t *testing.T) {
	locked := lockedModelCapabilitiesForConfig(
		"gpt-5",
		config.ModelCapabilitiesOverride{SupportsReasoningEffort: false},
		map[string]string{"model_capabilities.supports_reasoning_effort": "file"},
		"reviewer.model_capabilities.supports_reasoning_effort",
		"reviewer.model_capabilities.supports_vision_inputs",
	)

	if locked.SupportsReasoningEffort {
		t.Fatalf("expected inherited explicit reviewer reasoning false override to beat model contract, got %+v", locked)
	}
	if !locked.SupportsVisionInputs {
		t.Fatalf("expected default reviewer vision capability to come from model contract, got %+v", locked)
	}
}

type testLogger struct {
	lines []string
}

func (l *testLogger) Logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *testLogger) String() string {
	return strings.Join(l.lines, "\n")
}

func outsideNonTempDir(t *testing.T) string {
	t.Helper()
	return testsetup.NonTemporaryDirectory(
		t,
		"kent-runtimewire-outside-",
		tools.IsPathInTemporaryDir,
	)
}

func newRuntimeWireSession(t *testing.T, root string, name string) *session.Store {
	t.Helper()
	store, err := session.Create(root, name, root, sessioncontract.SessionCategoryMain, runtimeWireTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store %s: %v", name, err)
	}
	return store
}

func materializedRuntimeWireEventLog(t *testing.T, store *session.Store) session.MaterializedEventLog {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize runtimewire event log: %v", err)
	}
	return eventLog
}

func newRuntimeWireToolRegistry(t *testing.T, workspace string, enabled ...toolspec.ID) (*tools.Registry, *askquestion.AskQuestionBroker) {
	t.Helper()
	return newRuntimeWireLoggedToolRegistry(t, workspace, nil, enabled...)
}

func newRuntimeWireLoggedToolRegistry(t *testing.T, workspace string, logger Logger, enabled ...toolspec.ID) (*tools.Registry, *askquestion.AskQuestionBroker) {
	t.Helper()
	binding, broker, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   runtimewirefixture.FilesystemContext(t, workspace),
		Enabled:             enabled,
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
		Logger:              logger,
	})
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}
	return binding.Registry(), broker
}

func newRuntimeWireToolRegistryWithConfig(t *testing.T, workspace string, configRoot string, allowNonCwdEdits bool, enabled ...toolspec.ID) (*tools.Registry, *askquestion.AskQuestionBroker) {
	t.Helper()
	binding, broker, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   runtimewirefixture.FilesystemContext(t, workspace),
		Enabled:             enabled,
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		AllowNonCwdEdits:    allowNonCwdEdits,
		SupportsVision:      true,
		GlobalConfigDir:     configRoot,
	})
	if err != nil {
		t.Fatalf("build tool registry: %v", err)
	}
	return binding.Registry(), broker
}

func newRuntimeWireBinding(t *testing.T, workspace string, enabled ...toolspec.ID) *LocalToolRegistryBinding {
	t.Helper()
	binding, _, _, err := NewLocalToolRegistryBinding(LocalToolRegistryOptions{
		FilesystemContext:   runtimewirefixture.FilesystemContext(t, workspace),
		Enabled:             enabled,
		MinimumExecToBgTime: 15 * time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("new local tool registry binding: %v", err)
	}
	return binding
}

func newRuntimeWireEngine(t *testing.T, store *session.Store, client llm.Client, cfg ...runtime.Config) *runtime.Engine {
	t.Helper()
	engineConfig := runtime.Config{Model: "gpt-5"}
	if len(cfg) > 0 {
		engineConfig = cfg[0]
	}
	eng, err := runtime.New(
		store,
		materializedRuntimeWireEventLog(t, store),
		client,
		tools.NewRegistry(),
		engineConfig,
	)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := eng.Close(); closeErr != nil {
			t.Fatalf("close runtime: %v", closeErr)
		}
	})
	return eng
}

func shellPwdOutput(t *testing.T, registry *tools.Registry) string {
	t.Helper()
	handler, ok := registry.Get(toolspec.ToolExecCommand)
	if !ok {
		t.Fatal("expected exec_command handler")
	}
	result, err := handler.Call(context.Background(), tools.Call{ID: "call-pwd", Name: toolspec.ToolExecCommand, Input: json.RawMessage(`{"command":"pwd"}`)})
	if err != nil {
		t.Fatalf("exec_command call: %v", err)
	}
	var payload string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode exec_command output: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(payload), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected exec_command output, got %q", payload)
	}
	return canonicalPathForTest(t, strings.TrimSpace(lines[len(lines)-1]))
}

func canonicalPathForTest(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize path %q: %v", path, err)
	}
	return filepath.Clean(canonical)
}

type busyToggleFakeClient struct {
	mu        sync.Mutex
	responses []llm.Response
	calls     int
}

type runtimewireCaptureClient struct {
	mu        sync.Mutex
	caps      llm.ProviderCapabilities
	responses []llm.Response
	calls     []llm.Request
}

func (f *runtimewireCaptureClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("no fake response configured")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *runtimewireCaptureClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return f.caps, nil
}

func (f *runtimewireCaptureClient) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *busyToggleFakeClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.responses) == 0 {
		return llm.Response{}, errors.New("no fake response configured")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *busyToggleFakeClient) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
