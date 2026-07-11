package edit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"core/server/tools"
	patchtool "core/server/tools/patch"
	"core/shared/config"
)

const (
	maxEditableBytes = 100 * 1024 * 1024
	utf8BOM          = "\xef\xbb\xbf"
)

type Tool struct {
	workspaceRoot                string
	workspaceRootReal            string
	workspaceRootInfo            os.FileInfo
	workspaceOnly                bool
	allowOutsideWorkspace        bool
	outsideWorkspaceApprover     tools.FSGuardApprover
	outsideWorkspaceSessionMu    sync.RWMutex
	outsideWorkspaceSessionAllow bool
	pathDenyPolicy               tools.PathDenyPolicy
}

type resolvedPath struct {
	cleaned string
	real    string
	symlink bool
}

func New(workspaceRoot string, workspaceOnly bool, opts ...Option) (*Tool, error) {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, tools.WrapMissingWorkspaceRootError(abs, fmt.Errorf("resolve workspace real path: %w", err))
	}
	rootInfo, err := os.Stat(real)
	if err != nil {
		return nil, tools.WrapMissingWorkspaceRootError(abs, fmt.Errorf("stat workspace root: %w", err))
	}
	t := &Tool{workspaceRoot: abs, workspaceRootReal: real, workspaceRootInfo: rootInfo, workspaceOnly: workspaceOnly}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t, nil
}

func (t *Tool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	in, err := tools.ParseEditInput(c.Input)
	if err != nil {
		return editErrorResult(c, err), nil
	}
	resolved, err := t.resolvePath(ctx, in.Path)
	if err != nil {
		return editErrorResult(c, err), nil
	}
	unlock := tools.LockFSGuardPaths([]string{resolved.real})
	defer unlock()
	if err := t.apply(ctx, resolved, in); err != nil {
		return editErrorResult(c, err), nil
	}
	message := "ok"
	if resolved.symlink {
		message = "ok; warning: edited through symlink, real path is " + resolved.real + "; use that path directly next time"
	}
	return editSuccessResult(c, message), nil
}

func (t *Tool) apply(ctx context.Context, path resolvedPath, in tools.EditInput) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	info, statErr := os.Stat(path.real)
	if statErr == nil && info.IsDir() {
		return failf("path is a directory: %s.", path.real)
	}
	if in.OldString == "" {
		return t.create(path, in.NewString, info, statErr)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return failf("old_string matched 0 occurrences in %s. Provide exact current text or more context.", path.real)
	}
	if statErr != nil {
		return failf("stat path %s: %v", path.real, statErr)
	}
	if !info.Mode().IsRegular() {
		return failf("path is not a regular file: %s.", path.real)
	}
	if info.Size() > maxEditableBytes {
		return failf("maximum editable text file size is 100 MiB.")
	}
	if err := rejectBinaryExtension(path.real); err != nil {
		return err
	}
	original, err := os.ReadFile(path.real)
	if err != nil {
		return failf("read %s: %v", path.real, err)
	}
	text, bom, err := decodeText(original, path.real)
	if err != nil {
		return err
	}
	selection, err := selectReplacement(text, in)
	if err != nil {
		return failf("%s in %s. Provide exact current text or more context.", err.Error(), path.real)
	}
	updatedText := applyReplacement(text, selection)
	next := []byte(updatedText)
	if bom {
		next = append([]byte(utf8BOM), next...)
	}
	if bytes.Equal(next, original) {
		return failf("replacement produced no changes.")
	}
	if len(next) > maxEditableBytes {
		return failf("maximum editable text file size is 100 MiB.")
	}
	if err := writeAtomicallyIfUnchanged(path.real, next, info); err != nil {
		return err
	}
	return nil
}

func (t *Tool) create(path resolvedPath, newText string, info os.FileInfo, statErr error) error {
	if len([]byte(newText)) > maxEditableBytes {
		return failf("maximum editable text file size is 100 MiB.")
	}
	if err := rejectBinaryExtension(path.real); err != nil {
		return err
	}
	if hasMixedLineEndings(newText) {
		return failf("create content uses mixed line endings.")
	}
	if err := rejectBinaryBytes([]byte(newText), path.real); err != nil {
		return err
	}
	existingBOM := false
	if statErr == nil {
		if info.IsDir() {
			return failf("path is a directory: %s.", path.real)
		}
		if !info.Mode().IsRegular() {
			return failf("path is not a regular file: %s.", path.real)
		}
		if info.Size() > maxEditableBytes {
			return failf("maximum editable text file size is 100 MiB.")
		}
		data, err := os.ReadFile(path.real)
		if err != nil {
			return failf("read %s: %v", path.real, err)
		}
		text, bom, err := decodeText(data, path.real)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) != "" {
			return failf("target file already contains text: %s.", path.real)
		}
		existingBOM = bom
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return failf("stat path %s: %v", path.real, statErr)
	}
	next := []byte(newText)
	if existingBOM && !strings.HasPrefix(newText, utf8BOM) {
		next = append([]byte(utf8BOM), next...)
	}
	var before os.FileInfo
	if statErr == nil {
		before = info
	}
	if err := writeAtomicallyIfUnchanged(path.real, next, before); err != nil {
		return err
	}
	return nil
}

func decodeText(data []byte, path string) (string, bool, error) {
	bom := bytes.HasPrefix(data, []byte(utf8BOM))
	if bom {
		data = data[len(utf8BOM):]
	}
	if len(data) > maxEditableBytes {
		return "", false, failf("maximum editable text file size is 100 MiB.")
	}
	if err := rejectBinaryBytes(data, path); err != nil {
		return "", false, err
	}
	return string(data), bom, nil
}

func rejectBinaryBytes(data []byte, path string) error {
	if bytes.Contains(data, []byte{0}) {
		return failf("binary file rejected: %s.", path)
	}
	if !utf8.Valid(data) {
		return failf("invalid UTF-8 text file: %s.", path)
	}
	prefix := data
	if len(prefix) > 8192 {
		prefix = prefix[:8192]
	}
	for _, b := range prefix {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			return failf("binary file rejected: %s.", path)
		}
	}
	return nil
}

func rejectBinaryExtension(path string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".bmp", ".tiff", ".pdf", ".zip", ".gz", ".tgz", ".xz", ".bz2", ".7z", ".rar", ".tar", ".mp3", ".mp4", ".mov", ".avi", ".mkv", ".exe", ".dll", ".dylib", ".so", ".a", ".o", ".class", ".jar", ".wasm", ".woff", ".woff2", ".ttf", ".otf", ".sqlite", ".sqlite3", ".db":
		return failf("binary file extension rejected: %s.", path)
	default:
		return nil
	}
}

func hasMixedLineEndings(text string) bool {
	hasCRLF := strings.Contains(text, "\r\n")
	withoutCRLF := strings.ReplaceAll(text, "\r\n", "")
	hasLF := strings.Contains(withoutCRLF, "\n")
	hasCR := strings.Contains(withoutCRLF, "\r")
	styles := 0
	if hasCRLF {
		styles++
	}
	if hasLF {
		styles++
	}
	if hasCR {
		styles++
	}
	return styles > 1
}

func writeAtomicallyIfUnchanged(path string, data []byte, before os.FileInfo) error {
	if before != nil {
		current, err := os.Stat(path)
		if err != nil {
			return failf("target changed before commit: %s.", path)
		}
		if current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) || current.Mode().Perm() != before.Mode().Perm() {
			return failf("target changed before commit: %s.", path)
		}
	} else if _, err := os.Stat(path); err == nil {
		return failf("target appeared before commit: %s.", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return failf("stat path %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return failf("create parent dir for %s: %v", path, err)
	}
	mode := os.FileMode(0o644)
	if before != nil {
		mode = before.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".kent-edit-"+filepath.Base(path)+"-*")
	if err != nil {
		return failf("stage write failed: %v", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return failf("stage write failed: %v", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return failf("stage write failed: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return failf("stage write failed: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return failf("stage write failed: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return failf("commit write %s: %v", path, err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (t *Tool) outsideWorkspaceSessionAllowed() bool {
	t.outsideWorkspaceSessionMu.RLock()
	defer t.outsideWorkspaceSessionMu.RUnlock()
	return t.outsideWorkspaceSessionAllow
}

func (t *Tool) setOutsideWorkspaceSessionAllowed(allow bool) {
	t.outsideWorkspaceSessionMu.Lock()
	t.outsideWorkspaceSessionAllow = allow
	t.outsideWorkspaceSessionMu.Unlock()
}

func (t *Tool) resolvePath(ctx context.Context, requested string) (resolvedPath, error) {
	if runtime.GOOS == "windows" && (strings.HasPrefix(requested, `\\`) || strings.HasPrefix(requested, `//`)) {
		return resolvedPath{}, failf("UNC paths are not allowed.")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(t.workspaceRoot, candidate)
	}
	cleaned := filepath.Clean(candidate)
	approved := map[string]bool{}
	if _, err := t.outsideGuard().Allow(ctx, requested, cleaned, approved); err != nil {
		return resolvedPath{}, err
	}
	real, err := resolveRealTarget(cleaned)
	if err != nil {
		return resolvedPath{}, err
	}
	if approved[cleaned] && canReuseOutsideApproval(cleaned, real) {
		approved[real] = true
	}
	if _, err := t.outsideGuard().Allow(ctx, requested, real, approved); err != nil {
		return resolvedPath{}, err
	}
	return resolvedPath{cleaned: cleaned, real: real, symlink: t.isUserSymlink(cleaned, real)}, nil
}

func canReuseOutsideApproval(cleaned string, real string) bool {
	if cleaned == real {
		return true
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return info.Mode()&os.ModeSymlink == 0
}

func (t *Tool) isUserSymlink(cleaned string, real string) bool {
	if cleaned == real {
		return false
	}
	rel, err := filepath.Rel(t.workspaceRoot, cleaned)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	expected := filepath.Clean(filepath.Join(t.workspaceRootReal, rel))
	return expected != real
}

func resolveRealTarget(cleaned string) (string, error) {
	real, err := config.ResolveExistingAncestorRealPath(cleaned)
	if err != nil {
		return "", failf("resolve path %q: %v", cleaned, err)
	}
	return real, nil
}

func (t *Tool) outsideGuard() tools.FSGuard {
	return tools.NewFSGuard(tools.FSGuardConfig{
		WorkspaceRoot:         t.workspaceRoot,
		WorkspaceRootReal:     t.workspaceRootReal,
		WorkspaceRootInfo:     t.workspaceRootInfo,
		WorkspaceOnly:         t.workspaceOnly,
		AllowOutsideWorkspace: t.allowOutsideWorkspace,
		Approver:              t.outsideWorkspaceApprover,
		SessionAllowed:        t.outsideWorkspaceSessionAllowed,
		SetSessionAllowed:     t.setOutsideWorkspaceSessionAllowed,
		RejectionInstruction:  "If it's essential to the task, ask the user to make the edit manually at the end of the task.",
		ErrorLabels: tools.FSGuardErrorLabels{
			OutsidePath: "edit target outside workspace",
		},
		Failures: tools.FSGuardFailureFactory{
			NoPermission: func(path string, reason string) error {
				return failf("no file edit permission for %s. %s", path, reason)
			},
			DefaultApprovalFailed: func(path string, reason string) error {
				return failf("file edit approval failed for %s. %s", path, reason)
			},
			DefaultUserDenied: func(path string, commentary string) error {
				if strings.TrimSpace(commentary) == "" {
					return failf("user denied the edit for %s.", path)
				}
				return failf("user denied the edit for %s.\nUser said: %s", path, commentary)
			},
		},
		TemporaryPathAllowed: patchtool.IsPathInTemporaryDir,
		PathDenyPolicy:       t.pathDenyPolicy,
	})
}
