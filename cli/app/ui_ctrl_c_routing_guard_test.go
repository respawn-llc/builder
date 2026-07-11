package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeUICtrlCCallSitesRouteThroughCommonHelper(t *testing.T) {
	files, err := filepath.Glob("ui_*.go")
	if err != nil {
		t.Fatalf("glob runtime ui files: %v", err)
	}
	for _, file := range files {
		base := filepath.Base(file)
		if runtimeCtrlCGuardAllowlistedFile(base) {
			continue
		}
		t.Run(file, func(t *testing.T) {
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			lines := strings.Split(string(content), "\n")
			for index, line := range lines {
				if !runtimeCtrlCGuardLine(line) {
					continue
				}
				window := strings.Join(lines[index:min(index+9, len(lines))], "\n")
				if strings.Contains(window, "Preserve the normal interrupt/quit path below") {
					continue
				}
				if !strings.Contains(window, "handleRuntimeCtrlC(") {
					t.Fatalf("%s:%d handles Ctrl+C without handleRuntimeCtrlC:\n%s", file, index+1, window)
				}
			}
		})
	}
}

func runtimeCtrlCGuardLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "case tea.KeyCtrlC:" ||
		trimmed == `case "ctrl+c":` ||
		strings.Contains(trimmed, `== "ctrl+c"`)
}

func runtimeCtrlCGuardAllowlistedFile(file string) bool {
	switch file {
	case "ui_ctrl_c_routing_guard_test.go",
		"ui_key_adapter.go",
		"ui_keymap.go",
		"ui_runtime_ctrl_c.go":
		return true
	default:
		return strings.HasSuffix(file, "_test.go")
	}
}
