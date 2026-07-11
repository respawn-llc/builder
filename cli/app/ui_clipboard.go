package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

var clipboardPasteTimeout = 2 * time.Second
var clipboardTextCopyTimeout = 2 * time.Second

var pngHeader = [8]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type uiClipboardPasteTarget uint8

const (
	uiClipboardPasteTargetMain uiClipboardPasteTarget = iota
	uiClipboardPasteTargetAsk
)

type uiClipboardContent interface {
	uiClipboardContent()
}

type uiClipboardImage struct {
	Path     string
	lifetime uiClipboardImageLifetime
}

func (uiClipboardImage) uiClipboardContent() {}

type uiClipboardImageLifetime interface {
	discard() error
}

type uiClipboardTempImage struct {
	path    string
	remove  func(string) error
	removed bool
}

func (i *uiClipboardTempImage) discard() error {
	if i.removed {
		return nil
	}
	if err := i.remove(i.path); err != nil {
		return err
	}
	i.removed = true
	return nil
}

func newTemporaryClipboardImage(path string, lifetime *uiClipboardTempImage) uiClipboardImage {
	return uiClipboardImage{Path: path, lifetime: lifetime}
}

func (i uiClipboardImage) discard() error {
	if i.lifetime == nil {
		return errors.New("clipboard image has no lifetime")
	}
	return i.lifetime.discard()
}

type uiClipboardText struct {
	Text string
}

func (uiClipboardText) uiClipboardContent() {}

type uiClipboardPaster interface {
	Paste(context.Context) (uiClipboardContent, error)
}

type uiClipboardTextCopier interface {
	CopyText(context.Context, string) error
}

type uiClipboardPasteErrorKind uint8

const (
	uiClipboardPasteErrorNoContent uiClipboardPasteErrorKind = iota
	uiClipboardPasteErrorMissingTool
	uiClipboardPasteErrorUnsupported
	uiClipboardPasteErrorFailed
)

type uiClipboardPasteError struct {
	Kind    uiClipboardPasteErrorKind
	Message string
	Err     error
}

type clipboardPlatformEnvelope struct {
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	TextBase64 string `json:"textBase64"`
}

type uiClipboardCopyErrorKind uint8

const (
	uiClipboardCopyErrorMissingTool uiClipboardCopyErrorKind = iota
	uiClipboardCopyErrorUnsupported
	uiClipboardCopyErrorFailed
)

type uiClipboardCopyError struct {
	Kind    uiClipboardCopyErrorKind
	Message string
	Err     error
}

func (e *uiClipboardPasteError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *uiClipboardPasteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *uiClipboardCopyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *uiClipboardCopyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type uiClipboardCommandRunner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, name string, args ...string) error
	RunInput(ctx context.Context, input []byte, name string, args ...string) error
}

type execClipboardCommandRunner struct{}

func (execClipboardCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (execClipboardCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func (execClipboardCommandRunner) RunInput(ctx context.Context, input []byte, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Run()
}

type systemClipboardPaster struct {
	goos             string
	getenv           func(string) string
	lookPath         func(string) (string, error)
	runner           uiClipboardCommandRunner
	createTemp       func(string, string) (*os.File, error)
	writeFile        func(string, []byte, fs.FileMode) error
	remove           func(string) error
	openFile         func(string) (io.ReadCloser, error)
	preferredTempDir func() string
}

type systemClipboardTextCopier struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	runner   uiClipboardCommandRunner
}

func newSystemClipboardPaster() uiClipboardPaster {
	return &systemClipboardPaster{
		goos:       runtime.GOOS,
		getenv:     os.Getenv,
		lookPath:   exec.LookPath,
		runner:     execClipboardCommandRunner{},
		createTemp: os.CreateTemp,
		writeFile:  os.WriteFile,
		remove:     os.Remove,
		openFile: func(path string) (io.ReadCloser, error) {
			return os.Open(path)
		},
		preferredTempDir: defaultClipboardTempDir,
	}
}

func newSystemClipboardTextCopier() uiClipboardTextCopier {
	return &systemClipboardTextCopier{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		runner:   execClipboardCommandRunner{},
	}
}

func defaultClipboardTempDir() string {
	if runtime.GOOS != "windows" {
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			return "/tmp"
		}
	}
	return os.TempDir()
}

func (p *systemClipboardPaster) Paste(ctx context.Context) (uiClipboardContent, error) {
	switch p.goos {
	case "darwin":
		return p.pasteDarwin(ctx)
	case "linux":
		return p.pasteLinux(ctx)
	case "windows":
		return p.pasteWindows(ctx)
	default:
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorUnsupported, Message: fmt.Sprintf("Clipboard paste is unsupported on %s", p.goos)}
	}
}

func (p *systemClipboardPaster) pasteDarwin(ctx context.Context) (uiClipboardContent, error) {
	if err := requireClipboardTool(p.lookPath, "osascript"); err != nil {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorMissingTool, Message: "Clipboard paste on macOS requires `osascript`", Err: err}
	}
	path, temporaryImage, err := p.newTempPNGPath()
	if err != nil {
		return nil, err
	}
	output, err := p.runner.Output(ctx, "osascript", "-l", "JavaScript", "-e", darwinClipboardPasteScript(path))
	if err != nil {
		return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste failed", Err: err}, temporaryImage)
	}
	envelope, err := decodeClipboardPlatformEnvelope(output)
	if err != nil {
		return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned malformed content", Err: err}, temporaryImage)
	}
	switch envelope.Kind {
	case "image":
		if err := p.ensurePNGFile(path); err != nil {
			return nil, p.cleanupPasteError(err, temporaryImage)
		}
		return newTemporaryClipboardImage(filepath.Clean(path), temporaryImage), nil
	case "text":
		return p.textClipboardContent(envelope.Text, temporaryImage)
	case "empty":
		return p.textClipboardContent("", temporaryImage)
	default:
		return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned unsupported content"}, temporaryImage)
	}
}

func decodeClipboardPlatformEnvelope(output []byte) (clipboardPlatformEnvelope, error) {
	var envelope clipboardPlatformEnvelope
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return clipboardPlatformEnvelope{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return clipboardPlatformEnvelope{}, errors.New("clipboard envelope has trailing JSON content")
		}
		return clipboardPlatformEnvelope{}, err
	}
	if envelope.Kind == "" {
		return clipboardPlatformEnvelope{}, errors.New("clipboard envelope did not include a kind")
	}
	return envelope, nil
}

func darwinClipboardPasteScript(path string) string {
	quotedPath := strconv.Quote(path)
	return strings.Join([]string{
		`ObjC.import("AppKit");`,
		`ObjC.import("Foundation");`,
		`var path = $.NSString.stringWithUTF8String(` + quotedPath + `);`,
		`var pasteboard = $.NSPasteboard.generalPasteboard;`,
		`var png = pasteboard.dataForType($.NSPasteboardTypePNG);`,
		`if (png) {`,
		`  if (!png.writeToFileAtomically(path, true)) {`,
		`    throw new Error("could not write PNG clipboard image");`,
		`  }`,
		`  console.log(JSON.stringify({kind: "image"}));`,
		`} else {`,
		`  var tiff = pasteboard.dataForType($.NSPasteboardTypeTIFF);`,
		`  if (tiff) {`,
		`    var rep = $.NSBitmapImageRep.alloc.initWithData(tiff);`,
		`    if (!rep) { throw new Error("could not decode TIFF clipboard image"); }`,
		`    var encoded = rep.representationUsingTypeProperties($.NSPNGFileType, $({}));`,
		`    if (!encoded || !encoded.writeToFileAtomically(path, true)) { throw new Error("could not encode PNG clipboard image"); }`,
		`    console.log(JSON.stringify({kind: "image"}));`,
		`  } else {`,
		`    var text = pasteboard.stringForType($.NSPasteboardTypeString);`,
		`    if (text) {`,
		`      console.log(JSON.stringify({kind: "text", text: ObjC.unwrap(text)}));`,
		`    } else {`,
		`      console.log(JSON.stringify({kind: "empty"}));`,
		`    }`,
		`  }`,
		`}`,
	}, "\n")
}

var waylandClipboardTargets = []string{"image/png", "text/plain;charset=utf-8", "text/plain;charset=UTF-8", "UTF8_STRING", "text/plain"}
var x11ClipboardTargets = []string{"image/png", "UTF8_STRING", "text/plain;charset=utf-8", "text/plain;charset=UTF-8", "text/plain"}

func (p *systemClipboardPaster) pasteLinux(ctx context.Context) (uiClipboardContent, error) {
	wayland := strings.TrimSpace(p.getenv("WAYLAND_DISPLAY")) != ""
	x11 := strings.TrimSpace(p.getenv("DISPLAY")) != ""
	if wayland {
		if _, err := p.lookPath("wl-paste"); err == nil {
			return p.pasteLinuxTarget(ctx, "wl-paste", []string{"--list-types"}, func(target string) []string {
				return []string{"--no-newline", "--type", target}
			}, waylandClipboardTargets)
		}
	}
	if x11 {
		if _, err := p.lookPath("xclip"); err == nil {
			return p.pasteLinuxTarget(ctx, "xclip", []string{"-selection", "clipboard", "-target", "TARGETS", "-o"}, func(target string) []string {
				return []string{"-selection", "clipboard", "-target", target, "-o"}
			}, x11ClipboardTargets)
		}
	}
	if wayland {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorMissingTool, Message: "Clipboard paste on Wayland requires `wl-paste`"}
	}
	if x11 {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorMissingTool, Message: "Clipboard paste on X11 requires `xclip`"}
	}
	return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorUnsupported, Message: "Clipboard paste requires Wayland (`wl-paste`) or X11 (`xclip`)"}
}

func (p *systemClipboardPaster) pasteLinuxTarget(ctx context.Context, tool string, listArgs []string, readArgs func(string) []string, targets []string) (uiClipboardContent, error) {
	listing, err := p.runner.Output(ctx, tool, listArgs...)
	if err != nil {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste failed", Err: err}
	}
	target, found := selectClipboardTarget(listing, targets)
	if !found {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoContent, Message: "Clipboard does not contain supported content"}
	}
	data, err := p.runner.Output(ctx, tool, readArgs(target)...)
	if err != nil {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste failed", Err: err}
	}
	if target == "image/png" {
		image, err := p.savePNG(data)
		if err != nil {
			return nil, err
		}
		return image, nil
	}
	if len(data) == 0 {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoContent, Message: "Clipboard does not contain supported content"}
	}
	if !utf8.Valid(data) {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard text is not valid UTF-8"}
	}
	return uiClipboardText{Text: string(data)}, nil
}

func selectClipboardTarget(listing []byte, targets []string) (string, bool) {
	lines := strings.Split(string(listing), "\n")
	for _, target := range targets {
		for _, line := range lines {
			if strings.TrimSuffix(line, "\r") == target {
				return target, true
			}
		}
	}
	return "", false
}

func (p *systemClipboardPaster) pasteWindows(ctx context.Context) (uiClipboardContent, error) {
	powershell, err := findClipboardTool(p.lookPath, "pwsh", "powershell")
	if err != nil {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorMissingTool, Message: "Clipboard paste on Windows requires `pwsh` or `powershell`", Err: err}
	}
	path, temporaryImage, tempErr := p.newTempPNGPath()
	if tempErr != nil {
		return nil, tempErr
	}
	output, err := p.runner.Output(ctx, powershell, windowsClipboardPasteArgs(path)...)
	if err != nil {
		return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste failed", Err: err}, temporaryImage)
	}
	envelope, err := decodeWindowsClipboardEnvelope(output)
	if err != nil {
		return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned malformed content", Err: err}, temporaryImage)
	}
	switch envelope.Kind {
	case "image":
		if err := p.ensurePNGFile(path); err != nil {
			return nil, p.cleanupPasteError(err, temporaryImage)
		}
		return newTemporaryClipboardImage(filepath.Clean(path), temporaryImage), nil
	case "text":
		text, err := base64.StdEncoding.DecodeString(envelope.TextBase64)
		if err != nil {
			return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned malformed content", Err: err}, temporaryImage)
		}
		if !utf8.Valid(text) {
			return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned malformed content"}, temporaryImage)
		}
		return p.textClipboardContent(string(text), temporaryImage)
	case "empty":
		return p.textClipboardContent("", temporaryImage)
	default:
		return nil, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned unsupported content"}, temporaryImage)
	}
}

func decodeWindowsClipboardEnvelope(output []byte) (clipboardPlatformEnvelope, error) {
	for _, b := range output {
		if b > 0x7f {
			return clipboardPlatformEnvelope{}, errors.New("Windows clipboard envelope is not ASCII")
		}
	}
	return decodeClipboardPlatformEnvelope(output)
}

func windowsClipboardPasteArgs(path string) []string {
	command := windowsClipboardPasteScript(base64.StdEncoding.EncodeToString([]byte(path)))
	return []string{
		"-NoProfile",
		"-NonInteractive",
		"-STA",
		"-EncodedCommand",
		base64.StdEncoding.EncodeToString(utf16LEClipboardText(command)),
	}
}

func windowsClipboardPasteScript(encodedPath string) string {
	return strings.Join([]string{
		`$path = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('` + encodedPath + `'));`,
		`Add-Type -AssemblyName System.Windows.Forms;`,
		`Add-Type -AssemblyName System.Drawing;`,
		`[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false);`,
		`if ([System.Windows.Forms.Clipboard]::ContainsImage()) {`,
		`  $image = [System.Windows.Forms.Clipboard]::GetImage();`,
		`  if ($null -eq $image) { throw "clipboard image was unavailable"; }`,
		`  $image.Save($path, [System.Drawing.Imaging.ImageFormat]::Png);`,
		`  [Console]::Out.Write('{"kind":"image"}');`,
		`} elseif ([System.Windows.Forms.Clipboard]::ContainsText()) {`,
		`  $text = [System.Windows.Forms.Clipboard]::GetText();`,
		`  $encoded = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($text));`,
		`  [Console]::Out.Write('{"kind":"text","textBase64":"' + $encoded + '"}');`,
		`} else {`,
		`  [Console]::Out.Write('{"kind":"empty"}');`,
		`}`,
	}, "\n")
}

func requireClipboardTool(lookPath func(string) (string, error), name string) error {
	_, err := lookPath(name)
	return err
}

func findClipboardTool(lookPath func(string) (string, error), names ...string) (string, error) {
	var errs []error
	for _, name := range names {
		if _, err := lookPath(name); err == nil {
			return name, nil
		} else {
			errs = append(errs, err)
		}
	}
	return "", errors.Join(errs...)
}

func (p *systemClipboardPaster) newTempPNGPath() (string, *uiClipboardTempImage, error) {
	dir := os.TempDir()
	if p.preferredTempDir != nil {
		dir = p.preferredTempDir()
	}
	file, err := p.createTemp(dir, "kent-clipboard-*.png")
	if err != nil {
		return "", nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Could not create a clipboard image temp file", Err: err}
	}
	path := file.Name()
	temporaryImage := &uiClipboardTempImage{path: path, remove: p.remove}
	if closeErr := file.Close(); closeErr != nil {
		if cleanupErr := temporaryImage.discard(); cleanupErr != nil {
			return "", nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Could not create or remove clipboard image temp file", Err: errors.Join(closeErr, cleanupErr)}
		}
		return "", nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Could not create a clipboard image temp file", Err: closeErr}
	}
	return path, temporaryImage, nil
}

func (p *systemClipboardPaster) cleanupPasteError(cause error, temporaryImage *uiClipboardTempImage) error {
	if cleanupErr := temporaryImage.discard(); cleanupErr != nil {
		return &uiClipboardPasteError{
			Kind:    uiClipboardPasteErrorFailed,
			Message: "Clipboard paste failed and could not remove temporary image",
			Err:     errors.Join(cause, cleanupErr),
		}
	}
	return cause
}

func (p *systemClipboardPaster) textClipboardContent(text string, temporaryImage *uiClipboardTempImage) (uiClipboardContent, error) {
	if err := temporaryImage.discard(); err != nil {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Could not remove clipboard image temp file", Err: err}
	}
	if text == "" {
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoContent, Message: "Clipboard does not contain supported content"}
	}
	return uiClipboardText{Text: text}, nil
}

func (p *systemClipboardPaster) ensurePNGFile(path string) error {
	file, err := p.openFile(path)
	if err != nil {
		return &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste failed", Err: err}
	}
	header := make([]byte, len(pngHeader))
	_, readErr := io.ReadFull(file, header)
	closeErr := file.Close()
	if readErr != nil {
		return &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard image data is not PNG", Err: errors.Join(readErr, closeErr)}
	}
	if closeErr != nil {
		return &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Could not close clipboard image file", Err: closeErr}
	}
	if !bytes.Equal(header, pngHeader[:]) {
		return &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard image data is not PNG"}
	}
	return nil
}

func (p *systemClipboardPaster) savePNG(data []byte) (uiClipboardImage, error) {
	if len(data) == 0 {
		return uiClipboardImage{}, &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoContent, Message: "Clipboard does not contain an image"}
	}
	path, temporaryImage, err := p.newTempPNGPath()
	if err != nil {
		return uiClipboardImage{}, err
	}
	if err := p.writeFile(path, data, 0o600); err != nil {
		return uiClipboardImage{}, p.cleanupPasteError(&uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Could not save the clipboard image", Err: err}, temporaryImage)
	}
	if err := p.ensurePNGFile(path); err != nil {
		return uiClipboardImage{}, p.cleanupPasteError(err, temporaryImage)
	}
	return newTemporaryClipboardImage(filepath.Clean(path), temporaryImage), nil
}

func (p *systemClipboardTextCopier) CopyText(ctx context.Context, text string) error {
	switch p.goos {
	case "darwin":
		return p.copyDarwin(ctx, text)
	case "linux":
		return p.copyLinux(ctx, text)
	case "windows":
		return p.copyWindows(ctx, text)
	default:
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorUnsupported, Message: fmt.Sprintf("Clipboard copy is unsupported on %s", p.goos)}
	}
}

func (p *systemClipboardTextCopier) copyDarwin(ctx context.Context, text string) error {
	if err := requireClipboardTool(p.lookPath, "pbcopy"); err != nil {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorMissingTool, Message: "Clipboard copy on macOS requires `pbcopy`", Err: err}
	}
	if err := p.runner.RunInput(ctx, []byte(text), "pbcopy"); err != nil {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorFailed, Message: "Clipboard copy failed", Err: err}
	}
	return nil
}

func (p *systemClipboardTextCopier) copyLinux(ctx context.Context, text string) error {
	wayland := strings.TrimSpace(p.getenv("WAYLAND_DISPLAY")) != ""
	x11 := strings.TrimSpace(p.getenv("DISPLAY")) != ""
	if wayland {
		if _, err := p.lookPath("wl-copy"); err == nil {
			if err := p.runner.RunInput(ctx, []byte(text), "wl-copy", "--type", "text/plain;charset=utf-8"); err != nil {
				return &uiClipboardCopyError{Kind: uiClipboardCopyErrorFailed, Message: "Clipboard copy failed", Err: err}
			}
			return nil
		}
	}
	if x11 {
		if _, err := p.lookPath("xclip"); err == nil {
			if err := p.runner.RunInput(ctx, []byte(text), "xclip", "-selection", "clipboard"); err != nil {
				return &uiClipboardCopyError{Kind: uiClipboardCopyErrorFailed, Message: "Clipboard copy failed", Err: err}
			}
			return nil
		}
	}
	if wayland {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorMissingTool, Message: "Clipboard copy on Wayland requires `wl-copy`"}
	}
	if x11 {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorMissingTool, Message: "Clipboard copy on X11 requires `xclip`"}
	}
	return &uiClipboardCopyError{Kind: uiClipboardCopyErrorUnsupported, Message: "Clipboard copy requires Wayland (`wl-copy`) or X11 (`xclip`)"}
}

func (p *systemClipboardTextCopier) copyWindows(ctx context.Context, text string) error {
	clip, err := findClipboardTool(p.lookPath, "clip", "clip.exe")
	if err != nil {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorMissingTool, Message: "Clipboard copy on Windows requires `clip`", Err: err}
	}
	if err := p.runner.RunInput(ctx, utf16LEClipboardText(text), clip); err != nil {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorFailed, Message: "Clipboard copy failed", Err: err}
	}
	return nil
}

func utf16LEClipboardText(text string) []byte {
	encoded := utf16.Encode([]rune(text))
	if len(encoded) == 0 {
		return nil
	}
	buf := make([]byte, len(encoded)*2)
	for idx, unit := range encoded {
		binary.LittleEndian.PutUint16(buf[idx*2:], unit)
	}
	return buf
}

func isClipboardPasteKey(msg tea.KeyMsg) bool {
	if msg.Paste {
		return false
	}
	if msg.Type == tea.KeyCtrlV || msg.Type == tea.KeyCtrlD {
		return true
	}
	if msg.Type == tea.KeyRunes && msg.Alt && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'v', 'V', 'd', 'D':
			return true
		}
	}
	switch strings.ToLower(msg.String()) {
	case "ctrl+v", "ctrl+d", "alt+v", "alt+d":
		return true
	default:
		return false
	}
}

func (m *uiModel) pasteClipboardCmd(target uiClipboardPasteTarget) tea.Cmd {
	paster := m.clipboardPaster
	mainDraftToken := m.mainInputDraftToken
	askToken := m.ask.currentToken
	return func() tea.Msg {
		if paster == nil {
			return clipboardPasteDoneMsg{Target: target, MainDraftToken: mainDraftToken, AskToken: askToken, Err: &uiClipboardPasteError{Kind: uiClipboardPasteErrorUnsupported, Message: "Clipboard paste is unavailable"}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), clipboardPasteTimeout)
		defer cancel()
		content, err := paster.Paste(ctx)
		return clipboardPasteDoneMsg{Target: target, MainDraftToken: mainDraftToken, AskToken: askToken, Content: content, Err: err}
	}
}

func (m *uiModel) handleClipboardPasteDone(msg clipboardPasteDoneMsg) tea.Cmd {
	if msg.Err != nil {
		message, kind := clipboardPasteStatus(msg.Err)
		return m.sendTransientStatusWithNoticeID(message, kind, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	chars, err := clipboardContentRunes(msg.Content)
	if err != nil {
		message, kind := clipboardPasteStatus(err)
		return m.sendTransientStatusWithNoticeID(message, kind, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	switch msg.Target {
	case uiClipboardPasteTargetAsk:
		if !m.ask.hasCurrent() || !m.ask.freeform || msg.AskToken == 0 || msg.AskToken != m.ask.currentToken {
			return m.discardStaleClipboardImageCmd(msg.Content)
		}
		m.insertAskInputRunes(chars)
	default:
		if !m.inputMode().showsMainInput() || msg.MainDraftToken == 0 || msg.MainDraftToken != m.mainInputDraftToken {
			return m.discardStaleClipboardImageCmd(msg.Content)
		}
		return m.insertInputRunes(chars)
	}
	return nil
}

func (m *uiModel) discardStaleClipboardImageCmd(content uiClipboardContent) tea.Cmd {
	image, ok := content.(uiClipboardImage)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return clipboardImageDiscardDoneMsg{Err: image.discard()}
	}
}

func (m *uiModel) handleClipboardImageDiscardDone(msg clipboardImageDiscardDoneMsg) tea.Cmd {
	if msg.Err == nil {
		return nil
	}
	return m.sendTransientStatusWithNoticeID("Could not remove stale clipboard image", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}

func clipboardContentRunes(content uiClipboardContent) ([]rune, error) {
	switch content := content.(type) {
	case uiClipboardImage:
		if strings.TrimSpace(content.Path) == "" {
			return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned invalid image content"}
		}
		if content.lifetime == nil {
			return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned image without lifetime"}
		}
		return []rune(filepath.Clean(content.Path)), nil
	case uiClipboardText:
		if content.Text == "" {
			return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoContent, Message: "Clipboard does not contain supported content"}
		}
		return []rune(content.Text), nil
	default:
		return nil, &uiClipboardPasteError{Kind: uiClipboardPasteErrorFailed, Message: "Clipboard paste returned unsupported content"}
	}
}

func (m *uiModel) copyClipboardTextCmd(text string) tea.Cmd {
	return m.copyClipboardTextCmdForOperation(0, text)
}

func (m *uiModel) copyClipboardTextCmdForOperation(token uint64, text string) tea.Cmd {
	copier := m.clipboardTextCopier
	copyText := text
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardTextCopyTimeout)
		defer cancel()
		return clipboardTextCopyDoneMsg{token: token, Err: copyClipboardText(ctx, copier, copyText)}
	}
}

func copyClipboardText(ctx context.Context, copier uiClipboardTextCopier, text string) error {
	if copier == nil {
		return &uiClipboardCopyError{Kind: uiClipboardCopyErrorUnsupported, Message: "Clipboard copy is unavailable"}
	}
	return copier.CopyText(ctx, text)
}

func (m *uiModel) handleClipboardTextCopyDone(msg clipboardTextCopyDoneMsg) tea.Cmd {
	if msg.token != 0 {
		op := m.finalAnswerOperation
		if op == nil || op.token != msg.token || op.phase != uiFinalAnswerOperationClipboard {
			return nil
		}
		m.finalAnswerOperation = nil
	}
	if msg.Err != nil {
		message, kind := clipboardTextCopyStatus(msg.Err)
		return m.sendTransientStatusWithNoticeID(message, kind, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	return m.sendTransientStatusWithNoticeID("Copied final answer to clipboard", uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
}

func clipboardPasteStatus(err error) (string, uiStatusNoticeKind) {
	var pasteErr *uiClipboardPasteError
	if errors.As(err, &pasteErr) {
		return pasteErr.Message, uiStatusNoticeError
	}
	if err == nil {
		return "", uiStatusNoticeInfo
	}
	return "Clipboard paste failed", uiStatusNoticeError
}

func clipboardTextCopyStatus(err error) (string, uiStatusNoticeKind) {
	var copyErr *uiClipboardCopyError
	if errors.As(err, &copyErr) {
		return copyErr.Message, uiStatusNoticeError
	}
	if err == nil {
		return "", uiStatusNoticeInfo
	}
	return "Clipboard copy failed", uiStatusNoticeError
}
