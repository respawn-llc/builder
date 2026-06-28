package app

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"core/shared/config"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestModeTogglesUseDetailAltScreenNative(t *testing.T) {
	out := &bytes.Buffer{}
	seq := &bytes.Buffer{}
	var sequenceMu sync.Mutex
	originalSequenceWriter := writeTerminalSequence
	writeTerminalSequence = func(value string) {
		sequenceMu.Lock()
		_, _ = seq.WriteString(value)
		sequenceMu.Unlock()
	}
	defer func() { writeTerminalSequence = originalSequenceWriter }()
	model := newProjectedStaticUIModel(
		WithUIInitialTranscript([]UITranscriptEntry{{Role: "assistant", Text: "history marker"}}),
	)
	program := tea.NewProgram(model, tea.WithInput(strings.NewReader("")), tea.WithOutput(out), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	program.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	time.Sleep(20 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	time.Sleep(10 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	time.Sleep(20 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("program did not terminate")
	}
	raw := out.String()
	if !strings.Contains(raw, "\x1b[?1049h") || !strings.Contains(raw, "\x1b[?1049l") {
		t.Fatalf("expected detail alt-screen enter/leave sequences, got %q", raw)
	}
	if strings.Contains(raw, "\x1b[?1000h") || strings.Contains(raw, "\x1b[?1002h") || strings.Contains(raw, "\x1b[?1003h") || strings.Contains(raw, "\x1b[?1006h") {
		t.Fatalf("did not expect detail alt-screen to enable mouse capture because it blocks native selection, got %q", raw)
	}
	sequenceRaw := seq.String()
	if !strings.Contains(sequenceRaw, "\x1b[?1007h") || !strings.Contains(sequenceRaw, "\x1b[?1007l") {
		t.Fatalf("expected alternate-scroll enable/disable sequences, got %q", sequenceRaw)
	}
	plain := strings.Join(strings.Fields(xansi.Strip(raw)), " ")
	if !strings.Contains(plain, "history marker") {
		t.Fatalf("expected history marker to remain in output after mode toggles, got %q", plain)
	}
}

func TestModeTogglesUseDetailAltScreenAltMode(t *testing.T) {
	out := &bytes.Buffer{}
	seq := &bytes.Buffer{}
	var sequenceMu sync.Mutex
	originalSequenceWriter := writeTerminalSequence
	writeTerminalSequence = func(value string) {
		sequenceMu.Lock()
		_, _ = seq.WriteString(value)
		sequenceMu.Unlock()
	}
	defer func() { writeTerminalSequence = originalSequenceWriter }()
	model := newProjectedStaticUIModel()
	program := tea.NewProgram(model, tea.WithInput(strings.NewReader("")), tea.WithOutput(out), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	program.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	time.Sleep(20 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	time.Sleep(10 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	time.Sleep(20 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("program did not terminate")
	}
	raw := out.String()
	if !strings.Contains(raw, "\x1b[?1049h") || !strings.Contains(raw, "\x1b[?1049l") {
		t.Fatalf("expected detail alt-screen enter/leave sequences in alt config mode, got %q", raw)
	}
	if strings.Contains(raw, "\x1b[?1000h") || strings.Contains(raw, "\x1b[?1002h") || strings.Contains(raw, "\x1b[?1003h") || strings.Contains(raw, "\x1b[?1006h") {
		t.Fatalf("did not expect detail alt-screen to enable mouse capture because it blocks native selection, got %q", raw)
	}
	sequenceRaw := seq.String()
	if !strings.Contains(sequenceRaw, "\x1b[?1007h") || !strings.Contains(sequenceRaw, "\x1b[?1007l") {
		t.Fatalf("expected alternate-scroll enable/disable sequences in alt config mode, got %q", sequenceRaw)
	}
}

func TestMainUIStartsInNormalBufferAndShowsReplayAfterWindowSize(t *testing.T) {
	out := &bytes.Buffer{}
	settings := config.Settings{}
	model := newProjectedStaticUIModel(
		WithUIInitialTranscript([]UITranscriptEntry{{Role: "assistant", Text: "startup replay marker"}}),
	)
	program := tea.NewProgram(model, append(mainUIProgramOptionsWithOutput(settings, nil, nil, os.Stdout), tea.WithInput(strings.NewReader("")), tea.WithOutput(out), tea.WithoutSignals())...)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	time.Sleep(25 * time.Millisecond)
	program.Send(tea.WindowSizeMsg{Width: 100, Height: 28})
	time.Sleep(20 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("program did not terminate")
	}
	raw := out.String()
	if strings.Contains(raw, "\x1b[?1049h") || strings.Contains(raw, "\x1b[?1049l") {
		t.Fatalf("did not expect alt-screen sequences with always+native override, got %q", raw)
	}
	plain := strings.Join(strings.Fields(xansi.Strip(raw)), " ")
	if !strings.Contains(plain, "startup replay marker") {
		t.Fatalf("expected native replay text after first window size, got %q", plain)
	}
}

func TestNativeMainUIDefersLiveAreaUntilProgramWindowSize(t *testing.T) {
	nativeOut := &bytes.Buffer{}
	rendererOut := &bytes.Buffer{}
	model := newProjectedStaticUIModel(
		WithUINativeSurfaceWriter(nativeOut),
		WithUIInitialInput("startup width"),
	)
	program := tea.NewProgram(model, tea.WithInput(strings.NewReader("")), tea.WithOutput(rendererOut), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if nativeOut.String() != "" {
		t.Fatalf("native live area wrote before Bubble Tea delivered window size: %q", nativeOut.String())
	}

	program.Send(tea.WindowSizeMsg{Width: 100, Height: 28})
	time.Sleep(30 * time.Millisecond)
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("program did not terminate")
	}

	raw := nativeOut.String()
	if !strings.Contains(xansi.Strip(raw), strings.Repeat("─", 100)) {
		t.Fatalf("native live area did not render at program window width after window size, got %q", raw)
	}
	if strings.Contains(xansi.Strip(raw), strings.Repeat("─", 120)) {
		t.Fatalf("native live area rendered fallback-width frame, got %q", raw)
	}
}
