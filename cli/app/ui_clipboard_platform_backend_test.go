package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDarwinClipboardReadFunctionReturnsTextFromNamedPasteboard(t *testing.T) {
	path := darwinClipboardTestPath(t)
	text := "α\nβ"
	setup := namedDarwinPasteboardSetup(
		`pasteboard.setStringForType($.NSString.stringWithUTF8String(` + strconv.Quote(text) + `), $.NSPasteboardTypeString);`,
	)
	envelope := runDarwinClipboardReadFunction(t, setup, path)
	if envelope.Kind != "text" || envelope.Text != text {
		t.Fatalf("clipboard envelope = %#v, want text %q", envelope, text)
	}
}

func TestDarwinClipboardReadFunctionReturnsEmptyFromNamedPasteboard(t *testing.T) {
	path := darwinClipboardTestPath(t)
	setup := namedDarwinPasteboardSetup()
	envelope := runDarwinClipboardReadFunction(t, setup, path)
	if envelope.Kind != "empty" {
		t.Fatalf("clipboard envelope = %#v, want empty", envelope)
	}
}

func TestDarwinClipboardReadFunctionWritesPNGFromNamedPasteboard(t *testing.T) {
	path := darwinClipboardTestPath(t)
	setup := namedDarwinPasteboardSetup(
		`var png = $.NSData.alloc.initWithBase64EncodedStringOptions($("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9ZQmcAAAAASUVORK5CYII="), 0);`,
		`pasteboard.setDataForType(png, $.NSPasteboardTypePNG);`,
	)
	envelope := runDarwinClipboardReadFunction(t, setup, path)
	if envelope.Kind != "image" {
		t.Fatalf("clipboard envelope = %#v, want image", envelope)
	}
	assertClipboardPNGFile(t, path)
}

func TestDarwinClipboardReadFunctionConvertsTIFFFromNamedPasteboard(t *testing.T) {
	path := darwinClipboardTestPath(t)
	setup := namedDarwinPasteboardSetup(
		`var image = $.NSImage.imageNamed($.NSImageNameActionTemplate);`,
		`var tiff = image.TIFFRepresentation;`,
		`pasteboard.setDataForType(tiff, $.NSPasteboardTypeTIFF);`,
	)
	envelope := runDarwinClipboardReadFunction(t, setup, path)
	if envelope.Kind != "image" {
		t.Fatalf("clipboard envelope = %#v, want image", envelope)
	}
	assertClipboardPNGFile(t, path)
}

func darwinClipboardTestPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("requires the macOS pasteboard")
	}
	path := filepath.Join(t.TempDir(), "clipboard.png")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("reserve clipboard image path: %v", err)
	}
	return path
}

func namedDarwinPasteboardSetup(lines ...string) string {
	return strings.Join(append([]string{
		`var pasteboard = $.NSPasteboard.pasteboardWithUniqueName;`,
		`pasteboard.clearContents;`,
	}, lines...), "\n")
}

func assertClipboardPNGFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read clipboard image: %v", err)
	}
	if !bytes.HasPrefix(data, pngHeader[:]) {
		t.Fatalf("clipboard image is not PNG: header=%v", data[:min(len(data), len(pngHeader))])
	}
}

func runDarwinClipboardReadFunction(t *testing.T, setup string, path string) clipboardPlatformEnvelope {
	t.Helper()
	script := strings.Join([]string{
		`ObjC.import("AppKit");`,
		`ObjC.import("Foundation");`,
		darwinClipboardReadFunction,
		setup,
		`var path = $.NSString.stringWithUTF8String(` + strconv.Quote(path) + `);`,
		`JSON.stringify(kentReadClipboard(pasteboard, path));`,
	}, "\n")
	cmd := exec.Command("/usr/bin/osascript", "-l", "JavaScript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("run clipboard paste script: %v; stderr=%q", err, string(exitErr.Stderr))
		}
		t.Fatalf("run clipboard paste script: %v", err)
	}
	envelope, err := decodeClipboardPlatformEnvelope(output)
	if err != nil {
		t.Fatalf("decode clipboard paste output: %v; output_bytes=%d", err, len(output))
	}
	return envelope
}

func TestSystemClipboardPasterDarwinReturnsTextAndCleansReservedImagePath(t *testing.T) {
	paster, runner, dir := newTestSystemClipboardPaster(t, "darwin")
	paster.lookPath = stubLookPath("osascript")
	runner.outFn = func(name string, args ...string) ([]byte, error) {
		if name != "osascript" {
			t.Fatalf("command = %q, want osascript", name)
		}
		return []byte(`{"kind":"text","text":"α\nβ"}`), nil
	}

	content, err := paster.Paste(context.Background())
	if err != nil {
		t.Fatalf("paste clipboard: %v", err)
	}
	text, ok := content.(uiClipboardText)
	if !ok {
		t.Fatalf("content = %T, want uiClipboardText", content)
	}
	if got, want := text.Text, "α\nβ"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("reserved image file was not cleaned: %v", entries)
	}
}

func TestSystemClipboardPasterWindowsReturnsUnicodeTextThroughASCIIEnvelope(t *testing.T) {
	paster, runner, dir := newTestSystemClipboardPaster(t, "windows")
	paster.lookPath = stubLookPath("pwsh")
	text := "α\nβ"
	runner.outFn = func(name string, args ...string) ([]byte, error) {
		if name != "pwsh" {
			t.Fatalf("command = %q, want pwsh", name)
		}
		if len(args) != 5 || args[3] != "-EncodedCommand" {
			t.Fatalf("Windows clipboard invocation must use one encoded command, args=%q", args)
		}
		return []byte(`{"kind":"text","textBase64":"` + base64.StdEncoding.EncodeToString([]byte(text)) + `"}`), nil
	}
	runner.runFn = func(string, ...string) error {
		t.Fatal("Windows clipboard paste must return its envelope through stdout")
		return nil
	}

	content, err := paster.Paste(context.Background())
	if err != nil {
		t.Fatalf("paste clipboard: %v", err)
	}
	got, ok := content.(uiClipboardText)
	if !ok || got.Text != text {
		t.Fatalf("content = %#v, want text %q", content, text)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("reserved image file was not cleaned: %v", entries)
	}
}

type closeErrorClipboardFile struct {
	*bytes.Reader
}

func (closeErrorClipboardFile) Close() error {
	return errors.New("close failed")
}

func TestSystemClipboardPasterSurfacesClipboardImageCloseFailure(t *testing.T) {
	paster, _, _ := newTestSystemClipboardPaster(t, "darwin")
	paster.openFile = func(string) (io.ReadCloser, error) {
		return closeErrorClipboardFile{Reader: bytes.NewReader(pngHeader[:])}, nil
	}

	if err := paster.ensurePNGFile("/tmp/kent-clipboard.png"); err == nil {
		t.Fatal("expected clipboard image close failure")
	}
}

func TestSystemClipboardPasterWaylandSelectsExactTargetWithImagePrecedence(t *testing.T) {
	paster, runner, _ := newTestSystemClipboardPaster(t, "linux")
	paster.getenv = func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	paster.lookPath = stubLookPath("wl-paste")
	runner.outputs[clipboardCommandKey("wl-paste", "--list-types")] = []byte("text/plain;charset=utf-8\nimage/png\nimage/png;foo\n")
	runner.outputs[clipboardCommandKey("wl-paste", "--no-newline", "--type", "image/png")] = pngHeader[:]

	content, err := paster.Paste(context.Background())
	if err != nil {
		t.Fatalf("paste clipboard: %v", err)
	}
	if _, ok := content.(uiClipboardImage); !ok {
		t.Fatalf("content = %T, want image", content)
	}
}

func TestSystemClipboardPasterWaylandPreservesTextBytes(t *testing.T) {
	paster, runner, _ := newTestSystemClipboardPaster(t, "linux")
	paster.getenv = func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	paster.lookPath = stubLookPath("wl-paste")
	runner.outputs[clipboardCommandKey("wl-paste", "--list-types")] = []byte("text/plain;charset=utf-8\n")
	runner.outputs[clipboardCommandKey("wl-paste", "--no-newline", "--type", "text/plain;charset=utf-8")] = []byte("α\nβ\n")

	content, err := paster.Paste(context.Background())
	if err != nil {
		t.Fatalf("paste clipboard: %v", err)
	}
	text, ok := content.(uiClipboardText)
	if !ok || text.Text != "α\nβ\n" {
		t.Fatalf("content = %#v, want text with exact bytes", content)
	}
}

func TestSystemClipboardPasterRejectsInvalidLinuxTextUTF8(t *testing.T) {
	paster, runner, _ := newTestSystemClipboardPaster(t, "linux")
	paster.getenv = func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	paster.lookPath = stubLookPath("wl-paste")
	runner.outputs[clipboardCommandKey("wl-paste", "--list-types")] = []byte("text/plain\n")
	runner.outputs[clipboardCommandKey("wl-paste", "--no-newline", "--type", "text/plain")] = []byte{0xff}

	if _, err := paster.Paste(context.Background()); err == nil {
		t.Fatal("expected invalid UTF-8 clipboard text error")
	}
}

func TestSystemClipboardPasterX11PreservesTextWithoutImageTarget(t *testing.T) {
	paster, runner, _ := newTestSystemClipboardPaster(t, "linux")
	paster.getenv = func(name string) string {
		if name == "DISPLAY" {
			return ":0"
		}
		return ""
	}
	paster.lookPath = stubLookPath("xclip")
	runner.outputs[clipboardCommandKey("xclip", "-selection", "clipboard", "-target", "TARGETS", "-o")] = []byte("text/plain\nimage/jpeg\n")
	runner.outputs[clipboardCommandKey("xclip", "-selection", "clipboard", "-target", "text/plain", "-o")] = []byte("α\nβ\n")

	content, err := paster.Paste(context.Background())
	if err != nil {
		t.Fatalf("paste clipboard: %v", err)
	}
	text, ok := content.(uiClipboardText)
	if !ok || text.Text != "α\nβ\n" {
		t.Fatalf("content = %#v, want text with original newlines", content)
	}
}

func TestSystemClipboardPasterCleansReservedImagePathAfterMalformedPlatformEnvelope(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			paster, runner, dir := newTestSystemClipboardPaster(t, goos)
			if goos == "darwin" {
				paster.lookPath = stubLookPath("osascript")
			} else {
				paster.lookPath = stubLookPath("pwsh")
			}
			runner.outFn = func(string, ...string) ([]byte, error) {
				return []byte(`not-json`), nil
			}

			if _, err := paster.Paste(context.Background()); err == nil {
				t.Fatal("expected malformed envelope error")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read temporary directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("reserved image file was not cleaned: %v", entries)
			}
		})
	}
}

func TestSystemClipboardPasterSurfacesReservedImageCleanupFailure(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			paster, runner, _ := newTestSystemClipboardPaster(t, goos)
			if goos == "darwin" {
				paster.lookPath = stubLookPath("osascript")
			} else {
				paster.lookPath = stubLookPath("pwsh")
			}
			paster.remove = func(string) error {
				return errors.New("remove failed")
			}
			runner.outFn = func(string, ...string) ([]byte, error) {
				return []byte(`{"kind":"empty"}`), nil
			}

			if _, err := paster.Paste(context.Background()); err == nil {
				t.Fatal("expected temporary image cleanup error")
			}
		})
	}
}
