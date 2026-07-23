package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

type stubClipboardCommandRunner struct {
	outputs      map[string][]byte
	outErrs      map[string]error
	runErrs      map[string]error
	runInputErrs map[string]error
	runInputs    map[string][]byte
	commands     []string
	outFn        func(name string, args ...string) ([]byte, error)
	runFn        func(name string, args ...string) error
	runInputFn   func(input []byte, name string, args ...string) error
}

func (r *stubClipboardCommandRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := clipboardCommandKey(name, args...)
	r.commands = append(r.commands, key)
	if r.outFn != nil {
		return r.outFn(name, args...)
	}
	if err, ok := r.outErrs[key]; ok {
		return nil, err
	}
	if data, ok := r.outputs[key]; ok {
		return data, nil
	}
	return nil, errors.New("unexpected output command: " + key)
}

func (r *stubClipboardCommandRunner) Run(_ context.Context, name string, args ...string) error {
	key := clipboardCommandKey(name, args...)
	r.commands = append(r.commands, key)
	if r.runFn != nil {
		return r.runFn(name, args...)
	}
	if err, ok := r.runErrs[key]; ok {
		return err
	}
	return nil
}

func (r *stubClipboardCommandRunner) RunInput(_ context.Context, input []byte, name string, args ...string) error {
	key := clipboardCommandKey(name, args...)
	r.commands = append(r.commands, key)
	if r.runInputs == nil {
		r.runInputs = make(map[string][]byte)
	}
	r.runInputs[key] = append([]byte(nil), input...)
	if r.runInputFn != nil {
		return r.runInputFn(input, name, args...)
	}
	if err, ok := r.runInputErrs[key]; ok {
		return err
	}
	return nil
}

func clipboardCommandKey(name string, args ...string) string {
	key := name
	for _, arg := range args {
		key += "\x00" + arg
	}
	return key
}

func stubLookPath(available ...string) func(string) (string, error) {
	set := make(map[string]bool, len(available))
	for _, name := range available {
		set[name] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", fs.ErrNotExist
	}
}

func newTestSystemClipboardPaster(t *testing.T, goos string) (*systemClipboardPaster, *stubClipboardCommandRunner, string) {
	t.Helper()
	dir := t.TempDir()
	runner := &stubClipboardCommandRunner{
		outputs:      make(map[string][]byte),
		outErrs:      make(map[string]error),
		runErrs:      make(map[string]error),
		runInputErrs: make(map[string]error),
		runInputs:    make(map[string][]byte),
	}
	return &systemClipboardPaster{
		goos:       goos,
		getenv:     func(string) string { return "" },
		lookPath:   stubLookPath(),
		runner:     runner,
		createTemp: os.CreateTemp,
		writeFile:  os.WriteFile,
		remove:     os.Remove,
		openFile: func(path string) (io.ReadCloser, error) {
			return os.Open(path)
		},
		preferredTempDir: func() string { return dir },
	}, runner, dir
}

func newTestSystemClipboardTextCopier(goos string) (*systemClipboardTextCopier, *stubClipboardCommandRunner) {
	runner := &stubClipboardCommandRunner{
		outputs:      make(map[string][]byte),
		outErrs:      make(map[string]error),
		runErrs:      make(map[string]error),
		runInputErrs: make(map[string]error),
		runInputs:    make(map[string][]byte),
	}
	return &systemClipboardTextCopier{
		goos:     goos,
		getenv:   func(string) string { return "" },
		lookPath: stubLookPath(),
		runner:   runner,
	}, runner
}

func pasteClipboardImagePath(ctx context.Context, paster *systemClipboardPaster) (string, error) {
	content, err := paster.Paste(ctx)
	if err != nil {
		return "", err
	}
	image, ok := content.(uiClipboardImage)
	if !ok {
		return "", errors.New("clipboard paste did not return an image path")
	}
	return image.Path, nil
}

func TestSystemClipboardImagePasterLinuxWaylandUsesWLPaste(t *testing.T) {
	paster, runner, dir := newTestSystemClipboardPaster(t, "linux")
	paster.getenv = func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	paster.lookPath = stubLookPath("wl-paste")
	runner.outputs[clipboardCommandKey("wl-paste", "--list-types")] = []byte("image/png\n")
	runner.outputs[clipboardCommandKey("wl-paste", "--no-newline", "--type", "image/png")] = pngHeader[:]

	path, err := pasteClipboardImagePath(context.Background(), paster)
	if err != nil {
		t.Fatalf("paste image: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("expected temp path under %q, got %q", dir, path)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read pasted file: %v", readErr)
	}
	if !bytes.Equal(data, pngHeader[:]) {
		t.Fatalf("unexpected pasted file contents %q", string(data))
	}
	if got, want := runner.commands, []string{clipboardCommandKey("wl-paste", "--list-types"), clipboardCommandKey("wl-paste", "--no-newline", "--type", "image/png")}; !slices.Equal(got, want) {
		t.Fatalf("unexpected commands: %#v", runner.commands)
	}
}

func TestSystemClipboardImagePasterErrors(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		envKey    string
		envValue  string
		tools     []string
		configure func(*stubClipboardCommandRunner)
		want      uiClipboardPasteErrorKind
	}{
		{name: "Wayland missing tool", goos: "linux", envKey: "WAYLAND_DISPLAY", envValue: "wayland-0", want: uiClipboardPasteErrorMissingTool},
		{name: "Linux unsupported environment", goos: "linux", want: uiClipboardPasteErrorUnsupported},
		{
			name: "X11 no supported content", goos: "linux", envKey: "DISPLAY", envValue: ":0", tools: []string{"xclip"},
			configure: func(runner *stubClipboardCommandRunner) {
				runner.outputs[clipboardCommandKey("xclip", "-selection", "clipboard", "-target", "TARGETS", "-o")] = []byte("text/html\n")
			},
			want: uiClipboardPasteErrorNoContent,
		},
		{
			name: "X11 command failure", goos: "linux", envKey: "DISPLAY", envValue: ":0", tools: []string{"xclip"},
			configure: func(runner *stubClipboardCommandRunner) {
				runner.outErrs[clipboardCommandKey("xclip", "-selection", "clipboard", "-target", "TARGETS", "-o")] = errors.New("targets unavailable")
			},
			want: uiClipboardPasteErrorFailed,
		},
		{name: "Darwin missing tool", goos: "darwin", want: uiClipboardPasteErrorMissingTool},
		{name: "Windows missing tool", goos: "windows", want: uiClipboardPasteErrorMissingTool},
		{
			name: "Windows no supported content", goos: "windows", tools: []string{"pwsh"},
			configure: func(runner *stubClipboardCommandRunner) {
				runner.outFn = func(string, ...string) ([]byte, error) { return []byte(`{"kind":"empty"}`), nil }
			},
			want: uiClipboardPasteErrorNoContent,
		},
		{
			name: "Windows command failure", goos: "windows", tools: []string{"pwsh"},
			configure: func(runner *stubClipboardCommandRunner) {
				runner.outFn = func(string, ...string) ([]byte, error) { return nil, errors.New("powershell failed") }
			},
			want: uiClipboardPasteErrorFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paster, runner, _ := newTestSystemClipboardPaster(t, test.goos)
			if test.envKey != "" {
				paster.getenv = func(name string) string {
					if name == test.envKey {
						return test.envValue
					}
					return ""
				}
			}
			paster.lookPath = stubLookPath(test.tools...)
			if test.configure != nil {
				test.configure(runner)
			}
			_, err := paster.Paste(context.Background())
			var pasteErr *uiClipboardPasteError
			if !errors.As(err, &pasteErr) || pasteErr.Kind != test.want {
				t.Fatalf("Paste error = %v (%T), want kind %d", err, err, test.want)
			}
		})
	}
}

func TestSystemClipboardImagePasterDarwinUsesOsascript(t *testing.T) {
	paster, runner, dir := newTestSystemClipboardPaster(t, "darwin")
	paster.lookPath = stubLookPath("osascript")
	runner.outFn = func(name string, args ...string) ([]byte, error) {
		if name != "osascript" {
			return nil, errors.New("unexpected command: " + name)
		}
		if len(args) != 4 || args[0] != "-l" || args[1] != "JavaScript" || args[2] != "-e" {
			return nil, errors.New("unexpected osascript args")
		}
		path := filepath.Join(dir, "kent-clipboard-darwin-test.png")
		if err := os.WriteFile(path, pngHeader[:], 0o600); err != nil {
			return nil, err
		}
		return []byte(`{"kind":"image"}`), nil
	}
	paster.createTemp = func(string, string) (*os.File, error) {
		return os.Create(filepath.Join(dir, "kent-clipboard-darwin-test.png"))
	}

	path, err := pasteClipboardImagePath(context.Background(), paster)
	if err != nil {
		t.Fatalf("paste image: %v", err)
	}
	if got, want := path, filepath.Join(dir, "kent-clipboard-darwin-test.png"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected one command, got %#v", runner.commands)
	}
	if got := runner.commands[0]; !strings.HasPrefix(got, clipboardCommandKey("osascript", "-l", "JavaScript", "-e")) {
		t.Fatalf("expected osascript invocation, got %q", got)
	}
}

func TestSystemClipboardTextCopierDarwinUsesPbcopy(t *testing.T) {
	copier, runner := newTestSystemClipboardTextCopier("darwin")
	copier.lookPath = stubLookPath("pbcopy")

	if err := copier.CopyText(context.Background(), "final answer"); err != nil {
		t.Fatalf("copy text: %v", err)
	}
	key := clipboardCommandKey("pbcopy")
	if len(runner.commands) != 1 || runner.commands[0] != key {
		t.Fatalf("unexpected commands: %#v", runner.commands)
	}
	if got := string(runner.runInputs[key]); got != "final answer" {
		t.Fatalf("copied text = %q, want %q", got, "final answer")
	}
}

func TestSystemClipboardTextCopierLinuxWaylandUsesWLCopy(t *testing.T) {
	copier, runner := newTestSystemClipboardTextCopier("linux")
	copier.getenv = func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	copier.lookPath = stubLookPath("wl-copy")

	if err := copier.CopyText(context.Background(), "copied value"); err != nil {
		t.Fatalf("copy text: %v", err)
	}
	key := clipboardCommandKey("wl-copy", "--type", "text/plain;charset=utf-8")
	if len(runner.commands) != 1 || runner.commands[0] != key {
		t.Fatalf("unexpected commands: %#v", runner.commands)
	}
	if got := string(runner.runInputs[key]); got != "copied value" {
		t.Fatalf("copied text = %q, want %q", got, "copied value")
	}
}

func TestSystemClipboardTextCopierErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		goos string
		want uiClipboardCopyErrorKind
	}{
		{name: "Darwin missing tool", goos: "darwin", want: uiClipboardCopyErrorMissingTool},
		{name: "Linux unsupported environment", goos: "linux", want: uiClipboardCopyErrorUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			copier, _ := newTestSystemClipboardTextCopier(test.goos)
			err := copier.CopyText(context.Background(), "final answer")
			var copyErr *uiClipboardCopyError
			if !errors.As(err, &copyErr) || copyErr.Kind != test.want {
				t.Fatalf("CopyText error = %v (%T), want kind %d", err, err, test.want)
			}
		})
	}
}

func TestSystemClipboardTextCopierWindowsUsesClipWithUTF16LE(t *testing.T) {
	copier, runner := newTestSystemClipboardTextCopier("windows")
	copier.lookPath = stubLookPath("clip")
	text := "final ✓"

	if err := copier.CopyText(context.Background(), text); err != nil {
		t.Fatalf("copy text: %v", err)
	}
	key := clipboardCommandKey("clip")
	if len(runner.commands) != 1 || runner.commands[0] != key {
		t.Fatalf("unexpected commands: %#v", runner.commands)
	}
	wantUnits := utf16.Encode([]rune(text))
	want := make([]byte, len(wantUnits)*2)
	for idx, unit := range wantUnits {
		binary.LittleEndian.PutUint16(want[idx*2:], unit)
	}
	if got := runner.runInputs[key]; string(got) == text || len(got)%2 != 0 {
		t.Fatalf("expected UTF-16LE clipboard payload, got %#v", got)
	} else if !bytes.Equal(got, want) {
		t.Fatalf("clipboard bytes = %#v, want %#v", got, want)
	}
}
