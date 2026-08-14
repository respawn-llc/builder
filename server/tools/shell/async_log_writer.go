package shell

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	logWriterQueueChunks = 128
	logWriterFlushBytes  = 64 * 1024
	logWriterFlushDelay  = 25 * time.Millisecond
)

type asyncLogWriter struct {
	file    *os.File
	ch      chan []byte
	done    chan struct{}
	onFlush func()

	sendMu sync.RWMutex
	mu     sync.Mutex
	err    error
	closed bool
	once   sync.Once
}

func newAsyncLogWriter(file *os.File, onFlush func()) *asyncLogWriter {
	w := &asyncLogWriter{
		file:    file,
		ch:      make(chan []byte, logWriterQueueChunks),
		done:    make(chan struct{}),
		onFlush: onFlush,
	}
	go w.run()
	return w
}

func (w *asyncLogWriter) Write(p []byte) error {
	if w == nil || len(p) == 0 {
		return nil
	}
	if err := w.error(); err != nil {
		return err
	}
	chunk := append([]byte(nil), p...)
	w.sendMu.RLock()
	defer w.sendMu.RUnlock()
	if w.closed {
		return errors.New("shell log writer is closed")
	}
	w.ch <- chunk
	return w.error()
}

func (w *asyncLogWriter) Close() error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		w.sendMu.Lock()
		w.closed = true
		close(w.ch)
		w.sendMu.Unlock()
		<-w.done
	})
	return w.storedError()
}

func (w *asyncLogWriter) run() {
	defer close(w.done)
	var buf bytes.Buffer
	var timer *time.Timer
	var timerCh <-chan time.Time
	flush := func(syncFile bool) {
		flushed := false
		if buf.Len() > 0 && w.file != nil {
			if _, err := w.file.Write(buf.Bytes()); err != nil {
				w.recordError(fmt.Errorf("write shell log: %w", err))
			} else {
				flushed = true
			}
			buf.Reset()
		}
		if syncFile && w.file != nil {
			if err := w.file.Sync(); err != nil {
				w.recordError(fmt.Errorf("sync shell log: %w", err))
			}
			if err := w.file.Close(); err != nil {
				w.recordError(fmt.Errorf("close shell log: %w", err))
			}
			w.file = nil
		}
		if flushed && w.onFlush != nil {
			w.onFlush()
		}
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
			timerCh = nil
		}
	}
	armTimer := func() {
		if timer != nil {
			return
		}
		timer = time.NewTimer(logWriterFlushDelay)
		timerCh = timer.C
	}
	for {
		select {
		case chunk, ok := <-w.ch:
			if !ok {
				flush(true)
				return
			}
			if len(chunk) == 0 {
				continue
			}
			buf.Write(chunk)
			if buf.Len() >= logWriterFlushBytes {
				flush(false)
			} else {
				armTimer()
			}
		case <-timerCh:
			flush(false)
		}
	}
}

func (w *asyncLogWriter) error() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *asyncLogWriter) storedError() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *asyncLogWriter) recordError(err error) {
	if err == nil || w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
}
