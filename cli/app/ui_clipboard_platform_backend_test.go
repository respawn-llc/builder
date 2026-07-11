package app

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"
)

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
