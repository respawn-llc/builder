package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"core/server/llm/openaiwire"
)

type migrationSpoolWriter interface {
	io.Writer
	Chmod(os.FileMode) error
	Sync() error
	// Close consumes the handle even when it reports a flush/close error.
	Close() error
	Name() string
}

type migrationSpoolStorage interface {
	Create(dir string) (migrationSpoolWriter, error)
	Open(path string) (migrationSpoolReader, error)
	Remove(path string) error
}

type migrationSpoolReader interface {
	io.ReaderAt
	io.ReadCloser
}

type osMigrationSpoolStorage struct{}

func (osMigrationSpoolStorage) Create(dir string) (migrationSpoolWriter, error) {
	file, err := os.CreateTemp(dir, ".kent-session-value-*.spool")
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (osMigrationSpoolStorage) Open(path string) (migrationSpoolReader, error) {
	return openRegularSessionFile(path, "migration value spool")
}

func (osMigrationSpoolStorage) Remove(path string) error {
	return os.Remove(path)
}

type migrationValueStore struct {
	source       io.ReaderAt
	spoolDir     string
	ledger       *migrationResourceLedger
	storage      migrationSpoolStorage
	inlineBudget int64
}

func newMigrationValueStore(
	source io.ReaderAt,
	spoolDir string,
	ledger *migrationResourceLedger,
	storage migrationSpoolStorage,
) *migrationValueStore {
	return newMigrationValueStoreWithInlineBudget(
		source,
		spoolDir,
		ledger,
		storage,
		migrationInlineValueBudgetBytes,
	)
}

func newMigrationValueStoreWithInlineBudget(
	source io.ReaderAt,
	spoolDir string,
	ledger *migrationResourceLedger,
	storage migrationSpoolStorage,
	inlineBudget int64,
) *migrationValueStore {
	return &migrationValueStore{
		source:       source,
		spoolDir:     spoolDir,
		ledger:       ledger,
		storage:      storage,
		inlineBudget: inlineBudget,
	}
}

func (s *migrationValueStore) Lexical(
	valueRange migrationJSONValueRange,
) *migrationValueSource {
	return &migrationValueSource{
		source:     s.source,
		valueRange: valueRange,
		ledger:     s.ledger,
	}
}

func (s *migrationValueStore) Retain(
	valueRange migrationJSONValueRange,
) (*migrationValueSource, error) {
	if s == nil || s.source == nil {
		return nil, fmt.Errorf("migration value source is required")
	}
	if s.ledger == nil {
		return nil, fmt.Errorf("migration resource ledger is required")
	}
	if s.storage == nil {
		return nil, fmt.Errorf("migration spool storage is required")
	}
	if valueRange.Start < 0 || valueRange.End <= valueRange.Start {
		return nil, fmt.Errorf(
			"migration retained value range is invalid: [%d,%d)",
			valueRange.Start,
			valueRange.End,
		)
	}
	size := valueRange.Size()
	releaseInline, acquired, err := s.ledger.tryAcquireInline(size, s.inlineBudget)
	if err != nil {
		return nil, err
	}
	if acquired {
		inline := make([]byte, int(size))
		if _, err := s.source.ReadAt(inline, valueRange.Start); err != nil {
			releaseInline()
			return nil, fmt.Errorf("read inline migration value: %w", err)
		}
		return &migrationValueSource{
			inline:        inline,
			releaseInline: releaseInline,
			ledger:        s.ledger,
		}, nil
	}
	return s.retainSpool(size, func(destination io.Writer) error {
		return copyMigrationRangeWithBuffer(
			destination,
			s.source,
			valueRange,
			s.ledger,
		)
	})
}

func (s *migrationValueStore) RetainReader(
	source io.Reader,
	size int64,
) (*migrationValueSource, error) {
	if s == nil {
		return nil, fmt.Errorf("migration value store is required")
	}
	if source == nil {
		return nil, fmt.Errorf("migration retained reader is required")
	}
	if s.ledger == nil {
		return nil, fmt.Errorf("migration resource ledger is required")
	}
	if s.storage == nil {
		return nil, fmt.Errorf("migration spool storage is required")
	}
	if size < 0 {
		return nil, fmt.Errorf("migration retained reader size must not be negative: %d", size)
	}
	releaseInline, acquired, err := s.ledger.tryAcquireInline(size, s.inlineBudget)
	if err != nil {
		return nil, err
	}
	if acquired {
		inline := make([]byte, int(size))
		if _, err := io.ReadFull(source, inline); err != nil {
			releaseInline()
			return nil, fmt.Errorf("read inline migration value: %w", err)
		}
		return &migrationValueSource{
			inline:        inline,
			releaseInline: releaseInline,
			ledger:        s.ledger,
		}, nil
	}
	return s.retainSpool(size, func(destination io.Writer) error {
		return copyMigrationReaderWithBuffer(destination, source, size, s.ledger)
	})
}

func (s *migrationValueStore) retainSpool(
	size int64,
	copyValue func(io.Writer) error,
) (_ *migrationValueSource, resultErr error) {
	if copyValue == nil {
		return nil, fmt.Errorf("migration spool copy operation is required")
	}
	releaseOpen, err := s.ledger.acquireSpoolFile()
	if err != nil {
		return nil, err
	}
	writer, err := s.storage.Create(s.spoolDir)
	if err != nil {
		releaseOpen()
		return nil, fmt.Errorf("create migration value spool: %w", err)
	}
	path := writer.Name()
	written := int64(0)
	closed := false
	closeWriter := func() error {
		if closed {
			return nil
		}
		closeErr := writer.Close()
		releaseOpen()
		closed = true
		return wrapMigrationError("close migration value spool", closeErr)
	}
	cleanup := func() error {
		var errs []error
		if closeErr := closeWriter(); closeErr != nil {
			errs = append(errs, closeErr)
		}
		if removeErr := s.storage.Remove(path); removeErr != nil {
			errs = append(errs, fmt.Errorf("remove migration value spool: %w", removeErr))
		} else if ledgerErr := s.ledger.spoolRemoved(written); ledgerErr != nil {
			errs = append(errs, ledgerErr)
		}
		return errors.Join(errs...)
	}
	if err := writer.Chmod(0o600); err != nil {
		return nil, errors.Join(
			fmt.Errorf("set migration value spool mode: %w", err),
			cleanup(),
		)
	}
	countingWriter := migrationSpoolCountingWriter{
		writer:  writer,
		ledger:  s.ledger,
		written: &written,
	}
	if err := copyValue(&countingWriter); err != nil {
		return nil, errors.Join(err, cleanup())
	}
	if written != size {
		return nil, errors.Join(
			fmt.Errorf(
				"migration retained value size changed: copied=%d expected=%d",
				written,
				size,
			),
			cleanup(),
		)
	}
	if err := writer.Sync(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("sync migration value spool: %w", err),
			cleanup(),
		)
	}
	if err := closeWriter(); err != nil {
		return nil, errors.Join(
			err,
			cleanup(),
		)
	}
	return &migrationValueSource{
		spoolPath:    path,
		spoolSize:    written,
		spoolStorage: s.storage,
		ledger:       s.ledger,
	}, nil
}

type migrationSpoolCountingWriter struct {
	writer  io.Writer
	ledger  *migrationResourceLedger
	written *int64
}

func (w *migrationSpoolCountingWriter) Write(payload []byte) (int, error) {
	n, err := w.writer.Write(payload)
	if n > 0 {
		*w.written += int64(n)
		if ledgerErr := w.ledger.spoolGrew(int64(n)); ledgerErr != nil {
			return n, errors.Join(err, ledgerErr)
		}
	}
	return n, err
}

type migrationValueSource struct {
	source        io.ReaderAt
	valueRange    migrationJSONValueRange
	inline        []byte
	releaseInline func()
	spoolPath     string
	spoolSize     int64
	spoolStorage  migrationSpoolStorage
	ledger        *migrationResourceLedger
	closed        bool
}

func (s *migrationValueSource) Size() int64 {
	if s == nil {
		return 0
	}
	switch {
	case s.inline != nil:
		return int64(len(s.inline))
	case s.spoolPath != "":
		return s.spoolSize
	default:
		return s.valueRange.Size()
	}
}

func (s *migrationValueSource) CopyTo(dst io.Writer) error {
	if s == nil {
		return fmt.Errorf("migration value source is required")
	}
	if s.closed {
		return fmt.Errorf("migration value source is closed")
	}
	if dst == nil {
		return fmt.Errorf("migration value destination is required")
	}
	if s.inline != nil {
		return writeMigrationBytes(dst, s.inline)
	}
	if s.spoolPath == "" {
		return copyMigrationRangeWithBuffer(dst, s.source, s.valueRange, s.ledger)
	}
	reader, err := s.Open()
	if err != nil {
		return err
	}
	copyErr := copyMigrationReaderWithBuffer(dst, reader, s.Size(), s.ledger)
	return errors.Join(copyErr, reader.Close())
}

func (s *migrationValueSource) Open() (openaiwire.JSONSourceReader, error) {
	if s == nil {
		return nil, fmt.Errorf("migration value source is required")
	}
	if s.closed {
		return nil, fmt.Errorf("migration value source is closed")
	}
	switch {
	case s.inline != nil:
		return &migrationInlineValueReader{Reader: bytes.NewReader(s.inline)}, nil
	case s.spoolPath != "":
		releaseOpen, err := s.ledger.acquireSpoolFile()
		if err != nil {
			return nil, err
		}
		reader, err := s.spoolStorage.Open(s.spoolPath)
		if err != nil {
			releaseOpen()
			return nil, fmt.Errorf("open migration value spool: %w", err)
		}
		return &migrationValueReader{
			migrationSpoolReader: reader,
			release:              releaseOpen,
		}, nil
	default:
		return &migrationLexicalValueReader{SectionReader: io.NewSectionReader(
			s.source,
			s.valueRange.Start,
			s.valueRange.Size(),
		)}, nil
	}
}

type migrationInlineValueReader struct {
	*bytes.Reader
}

func (*migrationInlineValueReader) Close() error {
	return nil
}

type migrationLexicalValueReader struct {
	*io.SectionReader
}

func (*migrationLexicalValueReader) Close() error {
	return nil
}

type migrationValueReader struct {
	migrationSpoolReader
	release func()
	closed  bool
}

func (r *migrationValueReader) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	err := r.migrationSpoolReader.Close()
	r.release()
	return wrapMigrationError("close migration value spool", err)
}

func (s *migrationValueSource) Close() error {
	if s == nil || s.closed {
		return nil
	}
	if s.spoolPath != "" {
		if err := s.spoolStorage.Remove(s.spoolPath); err != nil {
			return fmt.Errorf("remove migration value spool: %w", err)
		}
		ledgerErr := s.ledger.spoolRemoved(s.spoolSize)
		s.spoolPath = ""
		s.spoolSize = 0
		if s.releaseInline != nil {
			s.releaseInline()
			s.releaseInline = nil
		}
		s.inline = nil
		s.closed = true
		return ledgerErr
	}
	if s.releaseInline != nil {
		s.releaseInline()
		s.releaseInline = nil
	}
	s.inline = nil
	s.closed = true
	return nil
}

func copyMigrationRangeWithBuffer(
	dst io.Writer,
	source io.ReaderAt,
	valueRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) error {
	if source == nil {
		return fmt.Errorf("migration range source is required")
	}
	if valueRange.Start < 0 || valueRange.End < valueRange.Start {
		return fmt.Errorf(
			"migration JSON value range is invalid: [%d,%d)",
			valueRange.Start,
			valueRange.End,
		)
	}
	return copyMigrationReaderWithBuffer(
		dst,
		io.NewSectionReader(source, valueRange.Start, valueRange.Size()),
		valueRange.Size(),
		ledger,
	)
}

func copyMigrationReaderWithBuffer(
	dst io.Writer,
	source io.Reader,
	size int64,
	ledger *migrationResourceLedger,
) error {
	release, err := ledger.acquireEncoderMerge(migrationCopyBufferBytes)
	if err != nil {
		return err
	}
	defer release()
	buffer := make([]byte, migrationCopyBufferBytes)
	remaining := size
	for remaining > 0 {
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		n, readErr := source.Read(buffer[:readSize])
		if n > 0 {
			if writeErr := writeMigrationBytes(dst, buffer[:n]); writeErr != nil {
				return writeErr
			}
			remaining -= int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && remaining == 0 {
				break
			}
			return fmt.Errorf("copy migration value: %w", readErr)
		}
		if n == 0 {
			return fmt.Errorf("copy migration value: %w", io.ErrNoProgress)
		}
	}
	return nil
}

func writeMigrationBytes(dst io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := dst.Write(payload)
		if n > 0 {
			payload = payload[n:]
		}
		if err != nil {
			return fmt.Errorf("write migration value: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("write migration value: %w", io.ErrNoProgress)
		}
	}
	return nil
}

func wrapMigrationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
