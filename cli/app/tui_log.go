package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	tuiLogDirName        = "logs"
	tuiLogFileName       = "tui.log"
	tuiLogMaxBytes int64 = 5 * 1024 * 1024
	tuiLogMaxFiles       = 5
)

type multiUILogger struct {
	loggers []uiLogger
}

func newMultiUILogger(loggers ...uiLogger) uiLogger {
	out := make([]uiLogger, 0, len(loggers))
	for _, logger := range loggers {
		if logger != nil {
			out = append(out, logger)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) == 1 {
		return out[0]
	}
	return multiUILogger{loggers: out}
}

func (l multiUILogger) Logf(format string, args ...any) {
	for _, logger := range l.loggers {
		logger.Logf(format, args...)
	}
}

type rollingTUILogger struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	fp       *os.File
	size     int64
}

func newRollingTUILogger(persistenceRoot string) (*rollingTUILogger, error) {
	root := strings.TrimSpace(persistenceRoot)
	if root == "" {
		return nil, nil
	}
	dir := filepath.Join(root, tuiLogDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create tui log dir: %w", err)
	}
	path := filepath.Join(dir, tuiLogFileName)
	fp, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open tui log: %w", err)
	}
	size := int64(0)
	if info, statErr := fp.Stat(); statErr == nil {
		size = info.Size()
	}
	return &rollingTUILogger{
		path:     path,
		maxBytes: tuiLogMaxBytes,
		maxFiles: tuiLogMaxFiles,
		fp:       fp,
		size:     size,
	}, nil
}

func (l *rollingTUILogger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fp == nil {
		return nil
	}
	err := l.fp.Close()
	l.fp = nil
	return err
}

func (l *rollingTUILogger) Logf(format string, args ...any) {
	if l == nil {
		return
	}
	line := strings.TrimRight(fmt.Sprintf(format, args...), "\r\n")
	if line == "" {
		return
	}
	payload := time.Now().UTC().Format(time.RFC3339Nano) + " " + line + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fp == nil {
		return
	}
	if l.maxBytes > 0 && l.size+int64(len(payload)) > l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			return
		}
	}
	if _, err := l.fp.WriteString(payload); err != nil {
		return
	}
	l.size += int64(len(payload))
}

func (l *rollingTUILogger) rotateLocked() error {
	if l.fp != nil {
		if err := l.fp.Close(); err != nil {
			return err
		}
		l.fp = nil
	}
	for idx := l.maxFiles - 1; idx >= 1; idx-- {
		src := rotatedTUILogPath(l.path, idx)
		dst := rotatedTUILogPath(l.path, idx+1)
		if idx == l.maxFiles-1 {
			_ = os.Remove(dst)
		}
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if _, err := os.Stat(l.path); err == nil {
		_ = os.Rename(l.path, rotatedTUILogPath(l.path, 1))
	}
	fp, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	l.fp = fp
	l.size = 0
	return nil
}

func rotatedTUILogPath(path string, generation int) string {
	if generation <= 0 {
		return path
	}
	return fmt.Sprintf("%s.%d", path, generation)
}
