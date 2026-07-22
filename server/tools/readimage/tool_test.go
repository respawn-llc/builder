package readimage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"core/server/tools"
	patchtool "core/server/tools/patch"
	"core/shared/toolspec"
)

var tinyPNG = []byte{
	137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 31, 21, 196, 137, 0, 0, 0, 11, 73, 68, 65, 84, 120, 156, 99, 96, 0, 2,
	0, 0, 5, 0, 1, 122, 94, 171, 63, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130,
}

func newReadImageTestTool(t *testing.T, workspace string, supported bool, opts ...Option) *Tool {
	t.Helper()
	tool, err := New(workspace, supported, opts...)
	if err != nil {
		t.Fatalf("new tool: %v", err)
	}
	return tool
}

func callReadImageTool(t *testing.T, tool *Tool, id string, input string) tools.Result {
	t.Helper()
	result, err := tool.Call(context.Background(), tools.Call{
		ID:    id,
		Name:  toolspec.ToolViewImage,
		Input: json.RawMessage(input),
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return result
}

func readImagePathInput(path string) string {
	return `{"path":"` + strings.ReplaceAll(path, `\`, `\\`) + `"}`
}

func writeReadImageTestFile(t *testing.T, workspace string, name string, data []byte) {
	t.Helper()
	writeReadImageTestPath(t, filepath.Join(workspace, name), data)
}

func writeReadImageTestPath(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}

func TestCall_ImagePathReturnsInputImageContentItem(t *testing.T) {
	workspace := t.TempDir()
	writeReadImageTestFile(t, workspace, "img.png", tinyPNG)

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-1", `{"path":"img.png"}`)
	if result.IsError {
		t.Fatalf("expected success result, got error payload: %s", string(result.Output))
	}

	var items []map[string]any
	if err := json.Unmarshal(result.Output, &items); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one content item, got %d", len(items))
	}
	if got := items[0]["type"]; got != "input_image" {
		t.Fatalf("expected input_image type, got %#v", got)
	}
	url, ok := items[0]["image_url"].(string)
	if !ok {
		t.Fatalf("expected image_url string, got %#v", items[0]["image_url"])
	}
	prefix := "data:image/png;base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("expected png data URL prefix, got %q", url)
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if decodeErr != nil {
		t.Fatalf("decode base64 image: %v", decodeErr)
	}
	if string(decoded) != string(tinyPNG) {
		t.Fatalf("decoded image bytes mismatch")
	}
}

func TestNewMissingWorkspaceSuggestsRebind(t *testing.T) {
	missingWorkspace := filepath.Join(t.TempDir(), "workspace-removed")

	_, err := New(missingWorkspace, true)
	if err == nil {
		t.Fatal("expected error for missing workspace")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
	want := `workspace root ` + strconv.Quote(missingWorkspace) + ` is missing`
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestNewSymlinkLoopWorkspaceReturnsContextualResolutionError(t *testing.T) {
	root := t.TempDir()
	loopPath := filepath.Join(root, "loop")
	if err := os.Symlink(loopPath, loopPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := New(loopPath, true)
	if err == nil {
		t.Fatal("expected error for symlink loop workspace")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected non-missing workspace error, got %v", err)
	}
	if !errors.Is(err, ErrResolveWorkspaceRealPath) {
		t.Fatalf("expected contextual resolution error, got %v", err)
	}
}

func TestCall_PDFPathReturnsInputFileContentItem(t *testing.T) {
	workspace := t.TempDir()
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")
	writeReadImageTestFile(t, workspace, "doc.pdf", pdfBytes)

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-1", `{"path":"doc.pdf"}`)
	if result.IsError {
		t.Fatalf("expected success result, got error payload: %s", string(result.Output))
	}

	var items []map[string]any
	if err := json.Unmarshal(result.Output, &items); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one content item, got %d", len(items))
	}
	if got := items[0]["type"]; got != "input_file" {
		t.Fatalf("expected input_file type, got %#v", got)
	}
	if got := items[0]["filename"]; got != "doc.pdf" {
		t.Fatalf("expected filename doc.pdf, got %#v", got)
	}
	encoded, ok := items[0]["file_data"].(string)
	if !ok {
		t.Fatalf("expected file_data string, got %#v", items[0]["file_data"])
	}
	const prefix = "data:application/pdf;base64,"
	if !strings.HasPrefix(encoded, prefix) {
		t.Fatalf("expected data URL prefix %q, got %q", prefix, encoded)
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, prefix))
	if decodeErr != nil {
		t.Fatalf("decode base64 file_data: %v", decodeErr)
	}
	if string(decoded) != string(pdfBytes) {
		t.Fatalf("decoded PDF bytes mismatch")
	}
}

func TestCall_UnsupportedFileReturnsToolError(t *testing.T) {
	workspace := t.TempDir()
	writeReadImageTestFile(t, workspace, "note.txt", []byte("hello"))

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-1", `{"path":"note.txt"}`)
	if !result.IsError {
		t.Fatalf("expected tool error result for unsupported file type")
	}
}

func TestCall_DirectoryPathReturnsToolError(t *testing.T) {
	workspace := t.TempDir()

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-1", `{"path":"."}`)
	if !result.IsError {
		t.Fatalf("expected tool error result for directory path")
	}
}

func TestCall_OversizedFileReturnsCompressionGuidance(t *testing.T) {
	workspace := t.TempDir()
	oversized := make([]byte, int(maxFileSizeBytes)+1)
	writeReadImageTestFile(t, workspace, "huge.pdf", oversized)

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-oversized", `{"path":"huge.pdf"}`)
	if !result.IsError {
		t.Fatalf("expected tool error result for oversized file")
	}
	errMessage := toolError(t, result)
	if !strings.Contains(errMessage, "max supported size is 819200 bytes (800 KiB)") {
		t.Fatalf("expected size limit in error, got %q", errMessage)
	}
	if !strings.Contains(errMessage, "compress the image or PDF and try again") {
		t.Fatalf("expected compression guidance in error, got %q", errMessage)
	}
}

func TestCall_FileSizeBoundary(t *testing.T) {
	workspace := t.TempDir()
	writeReadImageTestFile(t, workspace, "exact.pdf", make([]byte, int(maxFileSizeBytes)))
	writeReadImageTestFile(t, workspace, "oversized.pdf", make([]byte, int(maxFileSizeBytes)+1))

	tool := newReadImageTestTool(t, workspace, true)
	exactResult := callReadImageTool(t, tool, "call-exact-size", `{"path":"exact.pdf"}`)
	if exactResult.IsError {
		t.Fatalf("expected exact-size file to be allowed, got %s", string(exactResult.Output))
	}

	oversizedResult := callReadImageTool(t, tool, "call-oversized-size", `{"path":"oversized.pdf"}`)
	if !oversizedResult.IsError {
		t.Fatalf("expected oversized file to be rejected")
	}
}

func TestCall_UnsupportedModelReturnsToolError(t *testing.T) {
	workspace := t.TempDir()
	tool := newReadImageTestTool(t, workspace, false)
	result := callReadImageTool(t, tool, "call-1", `{"path":"img.png"}`)
	if !result.IsError {
		t.Fatalf("expected tool error result for unsupported model")
	}
}

func TestCall_PathTraversalOutsideWorkspaceRejectedByDefault(t *testing.T) {
	parent := outsideNonTempDir(t)
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	outsidePath := filepath.Join(parent, "outside.png")
	writeReadImageTestPath(t, outsidePath, tinyPNG)

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-traversal", `{"path":"../outside.png"}`)
	if !result.IsError {
		t.Fatalf("expected error for outside-workspace traversal path")
	}
	if !strings.Contains(toolError(t, result), "outside workspace") {
		t.Fatalf("expected outside workspace error, got %q", toolError(t, result))
	}
}

func TestCall_SymlinkEscapeOutsideWorkspaceRejectedByDefault(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(outsideNonTempDir(t), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)
	linkPath := filepath.Join(workspace, "symlink.png")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-symlink", `{"path":"symlink.png"}`)
	if !result.IsError {
		t.Fatalf("expected error for symlink escape outside workspace")
	}
	if !strings.Contains(toolError(t, result), "outside workspace") {
		t.Fatalf("expected outside workspace error, got %q", toolError(t, result))
	}
}

func TestCall_GeneratedPathReadUsesNormalOutsideWorkspaceApproval(t *testing.T) {
	workspace := t.TempDir()
	generatedRoot := filepath.Join(outsideNonTempDir(t), ".generated")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		t.Fatalf("mkdir generated root: %v", err)
	}
	generatedImage := filepath.Join(generatedRoot, "img.png")
	writeReadImageTestPath(t, generatedImage, tinyPNG)
	approvals := 0
	tool := newReadImageTestTool(t, workspace, true, WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
		approvals++
		return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowOnce}, nil
	}))

	result := callReadImageTool(t, tool, "generated-read", readImagePathInput(generatedImage))
	if result.IsError {
		t.Fatalf("expected generated read to follow normal approval, got %s", string(result.Output))
	}
	if approvals != 1 {
		t.Fatalf("outside read approvals = %d, want 1", approvals)
	}
}

func TestCall_OutsideWorkspaceTempDirAllowedWithoutApproval(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)

	approveCalls := 0
	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			approveCalls++
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}, nil
		}),
	)

	result := callReadImageTool(t, tool, "call-temp-allow", readImagePathInput(outside))
	if result.IsError {
		t.Fatalf("expected success for temp outside path, got %s", string(result.Output))
	}
	if approveCalls != 0 {
		t.Fatalf("expected temp outside path to bypass approver, got %d calls", approveCalls)
	}
}

func TestCall_OutsideWorkspaceAllowSessionSkipsFuturePrompts(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	outside1 := filepath.Join(outsideRoot, "outside1.png")
	outside2 := filepath.Join(outsideRoot, "outside2.png")
	writeReadImageTestPath(t, outside1, tinyPNG)
	writeReadImageTestPath(t, outside2, tinyPNG)

	approveCalls := 0
	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			approveCalls++
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowSession}, nil
		}),
	)

	result := callReadImageTool(t, tool, "call-1", readImagePathInput(outside1))
	if result.IsError {
		t.Fatalf("expected first call success, got %s", string(result.Output))
	}

	result = callReadImageTool(t, tool, "call-2", readImagePathInput(outside2))
	if result.IsError {
		t.Fatalf("expected second call success, got %s", string(result.Output))
	}

	if approveCalls != 1 {
		t.Fatalf("expected one approval call, got %d", approveCalls)
	}
}

func TestCall_OutsideWorkspaceAllowOncePromptsEachCall(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(outsideNonTempDir(t), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)

	approveCalls := 0
	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			approveCalls++
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowOnce}, nil
		}),
	)

	input := readImagePathInput(outside)
	result := callReadImageTool(t, tool, "call-1", input)
	if result.IsError {
		t.Fatalf("expected first call success, got %s", string(result.Output))
	}

	result = callReadImageTool(t, tool, "call-2", input)
	if result.IsError {
		t.Fatalf("expected second call success, got %s", string(result.Output))
	}

	if approveCalls != 2 {
		t.Fatalf("expected two approval calls, got %d", approveCalls)
	}
}

func TestCall_OutsideWorkspaceApprovalAuditsResolvedPath(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(outsideNonTempDir(t), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)

	audits := make([]OutsideWorkspaceAudit, 0, 2)
	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowSession}, nil
		}),
		WithOutsideWorkspaceAuditLogger(func(entry OutsideWorkspaceAudit) {
			audits = append(audits, entry)
		}),
	)

	input := readImagePathInput(outside)
	result := callReadImageTool(t, tool, "call-1", input)
	if result.IsError {
		t.Fatalf("expected first call success, got %s", string(result.Output))
	}

	result = callReadImageTool(t, tool, "call-2", input)
	if result.IsError {
		t.Fatalf("expected second call success, got %s", string(result.Output))
	}

	if len(audits) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(audits))
	}
	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolve outside real path: %v", err)
	}
	if audits[0].ResolvedPath != realOutside {
		t.Fatalf("unexpected first audit resolved path: %q", audits[0].ResolvedPath)
	}
	if audits[0].Reason != "allow_session" {
		t.Fatalf("unexpected first audit reason: %q", audits[0].Reason)
	}
	if audits[1].Reason != "session_allow" {
		t.Fatalf("unexpected second audit reason: %q", audits[1].Reason)
	}
}

func TestCall_OutsideWorkspaceApprovalFailureUsesReadSpecificWording(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(outsideNonTempDir(t), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)

	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			return patchtool.OutsideWorkspaceApproval{}, errors.New("ask failed")
		}),
	)

	result := callReadImageTool(t, tool, "call-approval-error", readImagePathInput(outside))
	if !result.IsError {
		t.Fatalf("expected error result")
	}
	errMessage := toolError(t, result)
	if !strings.Contains(errMessage, "outside-workspace read approval failed") {
		t.Fatalf("expected read approval failure wording, got %q", errMessage)
	}
	if strings.Contains(errMessage, "edit approval failed") || strings.Contains(errMessage, "patch target outside workspace") {
		t.Fatalf("unexpected patch wording, got %q", errMessage)
	}
}

func TestCall_OutsideWorkspaceRejectionIncludesReadSpecificGuidance(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(outsideNonTempDir(t), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)

	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny, Commentary: "keep it inside the repo"}, nil
		}),
	)

	result := callReadImageTool(t, tool, "call-deny-guidance", readImagePathInput(outside))
	if !result.IsError {
		t.Fatalf("expected error result")
	}
	errMessage := toolError(t, result)
	want := `view_image path outside workspace rejected by user: ` + outside + `. User rejected the approval request for this tool call, and said: "keep it inside the repo". Do not attempt to circumvent, hack around, or re-execute the same path. Treat this rejection as authoritative. If it's essential to the task, ask the user to place the file inside the workspace root.`
	if errMessage != want {
		t.Fatalf("unexpected rejection error, got %q want %q", errMessage, want)
	}
}

func TestCall_CaseVariantAbsolutePathInsideWorkspaceDoesNotTriggerOutsideApproval(t *testing.T) {
	workspace := t.TempDir()
	writeReadImageTestFile(t, workspace, "img.png", tinyPNG)

	variantWorkspace, ok := findCaseVariantExistingAlias(workspace)
	if !ok {
		t.Skip("filesystem does not provide a case-variant alias for workspace path")
	}
	variantImagePath := filepath.Join(variantWorkspace, "img.png")

	approveCalls := 0
	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
			approveCalls++
			return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}, nil
		}),
	)

	result := callReadImageTool(t, tool, "call-case-variant", readImagePathInput(variantImagePath))
	if result.IsError {
		t.Fatalf("expected success for case-variant absolute in-workspace path, got %s", string(result.Output))
	}
	if approveCalls != 0 {
		t.Fatalf("expected no outside-workspace approval prompts, got %d", approveCalls)
	}
}

func findCaseVariantExistingAlias(path string) (string, bool) {
	canonical := filepath.Clean(path)
	canonicalInfo, err := os.Stat(canonical)
	if err != nil {
		return "", false
	}
	if candidate, ok := caseAliasUsersSubstitution(canonical, canonicalInfo); ok {
		return candidate, true
	}

	parts := strings.Split(canonical, string(filepath.Separator))
	start := 0
	if filepath.IsAbs(canonical) && len(parts) > 0 && parts[0] == "" {
		start = 1
	}

	for idx := start; idx < len(parts); idx++ {
		variantPart := toggleFirstLetterCase(parts[idx])
		if variantPart == parts[idx] {
			continue
		}
		candidateParts := append([]string(nil), parts...)
		candidateParts[idx] = variantPart
		candidate := strings.Join(candidateParts, string(filepath.Separator))
		if candidate == canonical {
			continue
		}
		candidateInfo, statErr := os.Stat(candidate)
		if statErr != nil {
			continue
		}
		if os.SameFile(candidateInfo, canonicalInfo) {
			return candidate, true
		}
	}

	return "", false
}

func outsideNonTempDir(t *testing.T) string {
	t.Helper()
	bases := make([]string, 0, 2)
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		bases = append(bases, home)
	}
	for _, base := range bases {
		dir, err := os.MkdirTemp(base, "kent-readimage-outside-*")
		if err != nil {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			continue
		}
		if patchtool.IsPathInTemporaryDir(abs) {
			_ = os.RemoveAll(dir)
			continue
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		return abs
	}
	t.Skip("unable to create non-temporary outside directory for test")
	return ""
}

func caseAliasUsersSubstitution(canonical string, canonicalInfo os.FileInfo) (string, bool) {
	if strings.HasPrefix(canonical, "/Users/") {
		candidate := "/users/" + strings.TrimPrefix(canonical, "/Users/")
		if info, err := os.Stat(candidate); err == nil && os.SameFile(info, canonicalInfo) {
			return candidate, true
		}
	}
	if strings.HasPrefix(canonical, "/users/") {
		candidate := "/Users/" + strings.TrimPrefix(canonical, "/users/")
		if info, err := os.Stat(candidate); err == nil && os.SameFile(info, canonicalInfo) {
			return candidate, true
		}
	}
	return "", false
}

func toggleFirstLetterCase(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	first := runes[0]
	upper := unicode.ToUpper(first)
	lower := unicode.ToLower(first)
	if first == upper && first == lower {
		return value
	}
	if first == upper {
		runes[0] = lower
		return string(runes)
	}
	runes[0] = upper
	return string(runes)
}

func toolError(t *testing.T, result tools.Result) string {
	t.Helper()
	payload := map[string]string{}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode tool error output: %v", err)
	}
	return payload["error"]
}
