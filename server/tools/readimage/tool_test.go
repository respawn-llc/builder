package readimage

import (
	"context"
	"core/internal/testharness/runtimewirefixture"
	"core/internal/testharness/testsetup"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/imagefileio"
	"core/shared/toolspec"
)

var tinyPNG = []byte{
	137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 31, 21, 196, 137, 0, 0, 0, 11, 73, 68, 65, 84, 120, 156, 99, 96, 0, 2,
	0, 0, 5, 0, 1, 122, 94, 171, 63, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130,
}

func TestMain(m *testing.M) {
	if os.Getenv("KENT_TEST_BLOCK_IMAGE_FILE_READER") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	imagefileio.ExitIfWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	os.Exit(m.Run())
}

func newReadImageTestTool(t *testing.T, workspace string, supported bool, opts ...Option) *Tool {
	t.Helper()
	tool, err := New(runtimewirefixture.FilesystemContext(t, workspace), supported, opts...)
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

func TestCall_CancelledBlockedFileOpenReturnsToolError(t *testing.T) {
	workspace := t.TempDir()
	writeReadImageTestFile(t, workspace, "blocked.png", tinyPNG)
	t.Setenv("KENT_TEST_BLOCK_IMAGE_FILE_READER", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	result, err := newReadImageTestTool(t, workspace, true).Call(ctx, tools.Call{
		ID:    "call-blocked-open",
		Name:  toolspec.ToolViewImage,
		Input: json.RawMessage(`{"path":"blocked.png"}`),
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected blocked file open to return a tool error")
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("blocked file open returned after %s, want prompt cancellation", elapsed)
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

func TestCall_RelativeExistingFileOutsideWorkspaceUsesResolvedPathInReadSpecificError(t *testing.T) {
	parent := outsideNonTempDir(t)
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	outsidePath := filepath.Join(parent, "outside.png")
	writeReadImageTestPath(t, outsidePath, tinyPNG)

	tool := newReadImageTestTool(t, workspace, true)
	result := callReadImageTool(t, tool, "call-outside", readImagePathInput(filepath.Join("..", "outside.png")))
	if !result.IsError {
		t.Fatal("expected error for outside-workspace path")
	}
	got := toolError(t, result)
	if !strings.Contains(got, "view_image path outside workspace") {
		t.Fatalf("expected view_image outside-workspace error, got %q", got)
	}
	if !strings.Contains(got, outsidePath) {
		t.Fatalf("expected resolved path %q in error, got %q", outsidePath, got)
	}
}

func TestCall_OutsideWorkspaceApprovalProjectsRequestedAndResolvedPathsToAudit(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(outsideNonTempDir(t), "outside.png")
	writeReadImageTestPath(t, outside, tinyPNG)
	linkName := "outside-link.png"
	if err := os.Symlink(outside, filepath.Join(workspace, linkName)); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	var request tools.FileAccessRequest
	var audits []OutsideWorkspaceAudit
	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(_ context.Context, received tools.FileAccessRequest) (tools.FileAccessApproval, error) {
			request = received
			return tools.FileAccessApproval{Kind: tools.FileAccessApprovalAllowOnce}, nil
		}),
		WithOutsideWorkspaceAuditLogger(func(entry OutsideWorkspaceAudit) {
			audits = append(audits, entry)
		}),
	)

	result := callReadImageTool(t, tool, "call-audit", readImagePathInput(linkName))
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolve outside real path: %v", err)
	}
	wantRequest := tools.FileAccessRequest{
		RequestedPath:    linkName,
		ResolvedPath:     realOutside,
		WorkingDirectory: workspace,
	}
	if request != wantRequest {
		t.Fatalf("approval request = %+v, want %+v", request, wantRequest)
	}
	if len(audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(audits))
	}
	wantAudit := OutsideWorkspaceAudit{
		RequestedPath: linkName,
		ResolvedPath:  realOutside,
		Reason:        tools.FileAccessReasonAllowOnce,
	}
	if audits[0] != wantAudit {
		t.Fatalf("audit = %+v, want %+v", audits[0], wantAudit)
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
		WithOutsideWorkspaceApprover(func(context.Context, tools.FileAccessRequest) (tools.FileAccessApproval, error) {
			return tools.FileAccessApproval{}, errors.New("ask failed")
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
	commentary := "keep it inside the repo"

	tool := newReadImageTestTool(
		t,
		workspace,
		true,
		WithOutsideWorkspaceApprover(func(context.Context, tools.FileAccessRequest) (tools.FileAccessApproval, error) {
			return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny, Commentary: &commentary}, nil
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

func outsideNonTempDir(t *testing.T) string {
	t.Helper()
	return testsetup.NonTemporaryDirectory(
		t,
		"kent-readimage-outside-",
		tools.IsPathInTemporaryDir,
	)
}

func toolError(t *testing.T, result tools.Result) string {
	t.Helper()
	payload := map[string]string{}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode tool error output: %v", err)
	}
	return payload["error"]
}
