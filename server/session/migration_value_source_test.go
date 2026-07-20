package session

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"core/internal/testharness/filemode"
)

func TestMigrationValueStoreUsesInlineBudgetThenMode0600Spool(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.json")
	sourceBytes := bytes.Repeat([]byte("x"), migrationInlineValueBudgetBytes+1)
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatalf("write value source fixture: %v", err)
	}
	source, err := openRegularSessionFile(sourcePath, "value source fixture")
	if err != nil {
		t.Fatalf("open value source fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	store := newMigrationValueStore(source, dir, ledger, osMigrationSpoolStorage{})
	inline, err := store.Retain(migrationJSONValueRange{
		Start: 0,
		End:   migrationInlineValueBudgetBytes,
	})
	if err != nil {
		t.Fatalf("retain inline migration value: %v", err)
	}
	defer inline.Close()
	spooled, err := store.Retain(migrationJSONValueRange{
		Start: migrationInlineValueBudgetBytes,
		End:   migrationInlineValueBudgetBytes + 1,
	})
	if err != nil {
		t.Fatalf("retain spooled migration value: %v", err)
	}
	if spooled.spoolPath == "" {
		t.Fatal("overflow value did not spool")
	}
	filemode.AssertUnixPermissionMode(t, spooled.spoolPath, 0o600)

	var copied bytes.Buffer
	if err := inline.CopyTo(&copied); err != nil {
		t.Fatalf("copy inline migration value: %v", err)
	}
	if err := spooled.CopyTo(&copied); err != nil {
		t.Fatalf("copy spooled migration value: %v", err)
	}
	if !bytes.Equal(copied.Bytes(), sourceBytes) {
		t.Fatal("inline/spooled migration values changed bytes")
	}
	stats := ledger.snapshot()
	if stats.MaxLiveInlineBytes != migrationInlineValueBudgetBytes {
		t.Fatalf(
			"maximum inline bytes = %d, want %d",
			stats.MaxLiveInlineBytes,
			migrationInlineValueBudgetBytes,
		)
	}
	if stats.CurrentSpoolBytes != 1 || stats.PeakSpoolBytes != 1 {
		t.Fatalf("spool byte stats = %+v, want current=1 peak=1", stats)
	}
	if stats.MaxOpenSpoolFiles != 1 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("spool file stats = %+v, want max=1 current=0", stats)
	}

	spoolPath := spooled.spoolPath
	if err := spooled.Close(); err != nil {
		t.Fatalf("close spooled migration value: %v", err)
	}
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool path remains after close: %v", err)
	}
	if err := inline.Close(); err != nil {
		t.Fatalf("close inline migration value: %v", err)
	}
	stats = ledger.snapshot()
	if stats.LiveInlineBytes != 0 || stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("resources remain after value close: %+v", stats)
	}
}

func TestMigrationValueSourceCopiesLexicalRangeDirectly(t *testing.T) {
	sourceBytes := []byte(`prefix{ "escaped" : "\u0061", "number" : 1.2300 }suffix`)
	path := filepath.Join(t.TempDir(), "source.json")
	if err := os.WriteFile(path, sourceBytes, 0o600); err != nil {
		t.Fatalf("write lexical source fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "lexical source fixture")
	if err != nil {
		t.Fatalf("open lexical source fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	store := newMigrationValueStore(source, t.TempDir(), ledger, osMigrationSpoolStorage{})
	value := store.Lexical(migrationJSONValueRange{
		Start: int64(len("prefix")),
		End:   int64(len(sourceBytes) - len("suffix")),
	})
	var copied bytes.Buffer
	if err := value.CopyTo(&copied); err != nil {
		t.Fatalf("copy lexical migration value: %v", err)
	}
	want := []byte(`{ "escaped" : "\u0061", "number" : 1.2300 }`)
	if !bytes.Equal(copied.Bytes(), want) {
		t.Fatalf("copied lexical bytes = %q, want %q", copied.Bytes(), want)
	}
	stats := ledger.snapshot()
	if stats.MaxLiveInlineBytes != 0 || stats.PeakSpoolBytes != 0 {
		t.Fatalf("lexical range unexpectedly retained or spooled: %+v", stats)
	}
	if stats.MaxEncoderMergeBytes != migrationCopyBufferBytes {
		t.Fatalf(
			"maximum encoder bytes = %d, want %d",
			stats.MaxEncoderMergeBytes,
			migrationCopyBufferBytes,
		)
	}
}

func TestMigrationValueSourceDoesNotDelegateCopyBufferToDestination(t *testing.T) {
	sourceBytes := bytes.Repeat([]byte("x"), migrationCopyBufferBytes+1)
	path := filepath.Join(t.TempDir(), "source.json")
	if err := os.WriteFile(path, sourceBytes, 0o600); err != nil {
		t.Fatalf("write lexical source fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "lexical source fixture")
	if err != nil {
		t.Fatalf("open lexical source fixture: %v", err)
	}
	defer source.Close()
	ledger := newMigrationResourceLedger()
	store := newMigrationValueStore(source, t.TempDir(), ledger, osMigrationSpoolStorage{})
	value := store.Lexical(migrationJSONValueRange{Start: 0, End: int64(len(sourceBytes))})
	destination := &readerFromMigrationDestination{}
	if err := value.CopyTo(destination); err != nil {
		t.Fatalf("copy lexical migration value: %v", err)
	}
	if destination.readFromCalled {
		t.Fatal("migration copy delegated to destination ReaderFrom")
	}
	if !bytes.Equal(destination.bytes, sourceBytes) {
		t.Fatal("manual migration copy changed bytes")
	}
	if stats := ledger.snapshot(); stats.MaxEncoderMergeBytes != migrationCopyBufferBytes {
		t.Fatalf("manual copy resource stats = %+v", stats)
	}
}

func TestMigrationValueStoreCleansSpoolAfterENOSPC(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.json")
	if err := os.WriteFile(sourcePath, []byte(`"spool me"`), 0o600); err != nil {
		t.Fatalf("write value source fixture: %v", err)
	}
	source, err := openRegularSessionFile(sourcePath, "value source fixture")
	if err != nil {
		t.Fatalf("open value source fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	storage := &failingMigrationSpoolStorage{
		delegate: osMigrationSpoolStorage{},
		writeErr: syscall.ENOSPC,
	}
	store := newMigrationValueStoreWithInlineBudget(source, dir, ledger, storage, 0)
	_, err = store.Retain(migrationJSONValueRange{Start: 0, End: int64(len(`"spool me"`))})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("retain error = %T %v, want ENOSPC", err, err)
	}
	stats := ledger.snapshot()
	if stats.OpenSpoolFiles != 0 || stats.CurrentSpoolBytes != 0 {
		t.Fatalf("failed spool leaked resources: %+v", stats)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read spool directory: %v", readErr)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(sourcePath) {
			t.Fatalf("failed spool left artifact %q", entry.Name())
		}
	}
}

func TestMigrationValueStoreInlineAndSpoolEncodingAreEquivalent(t *testing.T) {
	sourceBytes := []byte(`{ "escaped" : "\u0061", "number" : 1.2300 }`)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.json")
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatalf("write value source fixture: %v", err)
	}
	source, err := openRegularSessionFile(sourcePath, "value source fixture")
	if err != nil {
		t.Fatalf("open value source fixture: %v", err)
	}
	defer source.Close()
	valueRange := migrationJSONValueRange{Start: 0, End: int64(len(sourceBytes))}

	inlineLedger := newMigrationResourceLedger()
	inlineStore := newMigrationValueStore(
		source,
		dir,
		inlineLedger,
		osMigrationSpoolStorage{},
	)
	inline, err := inlineStore.Retain(valueRange)
	if err != nil {
		t.Fatalf("retain inline value: %v", err)
	}
	defer inline.Close()

	spoolLedger := newMigrationResourceLedger()
	spoolStore := newMigrationValueStoreWithInlineBudget(
		source,
		dir,
		spoolLedger,
		osMigrationSpoolStorage{},
		0,
	)
	spooled, err := spoolStore.Retain(valueRange)
	if err != nil {
		t.Fatalf("retain spooled value: %v", err)
	}
	defer spooled.Close()

	var inlineEncoded bytes.Buffer
	if err := inline.CopyTo(&inlineEncoded); err != nil {
		t.Fatalf("encode inline value: %v", err)
	}
	var spoolEncoded bytes.Buffer
	if err := spooled.CopyTo(&spoolEncoded); err != nil {
		t.Fatalf("encode spooled value: %v", err)
	}
	if !bytes.Equal(inlineEncoded.Bytes(), spoolEncoded.Bytes()) ||
		!bytes.Equal(inlineEncoded.Bytes(), sourceBytes) {
		t.Fatalf(
			"inline/spooled bytes differ: inline=%q spooled=%q source=%q",
			inlineEncoded.Bytes(),
			spoolEncoded.Bytes(),
			sourceBytes,
		)
	}
}

func TestMigrationValueStoreSpoolLifecycleFailuresReleaseResources(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*failingMigrationSpoolStorage)
		wantErr   error
	}{
		{
			name: "create",
			configure: func(storage *failingMigrationSpoolStorage) {
				storage.createErr = syscall.ENOSPC
			},
			wantErr: syscall.ENOSPC,
		},
		{
			name: "sync",
			configure: func(storage *failingMigrationSpoolStorage) {
				storage.syncErr = syscall.EIO
			},
			wantErr: syscall.EIO,
		},
		{
			name: "close",
			configure: func(storage *failingMigrationSpoolStorage) {
				storage.closeErr = syscall.EIO
			},
			wantErr: syscall.EIO,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			sourcePath := filepath.Join(dir, "source.json")
			sourceBytes := []byte(`"spool lifecycle"`)
			if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
				t.Fatalf("write value source fixture: %v", err)
			}
			source, err := openRegularSessionFile(sourcePath, "value source fixture")
			if err != nil {
				t.Fatalf("open value source fixture: %v", err)
			}
			defer source.Close()
			ledger := newMigrationResourceLedger()
			storage := &failingMigrationSpoolStorage{
				delegate: osMigrationSpoolStorage{},
			}
			test.configure(storage)
			store := newMigrationValueStoreWithInlineBudget(source, dir, ledger, storage, 0)
			_, err = store.Retain(migrationJSONValueRange{
				Start: 0,
				End:   int64(len(sourceBytes)),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("retain error = %T %v, want %v", err, err, test.wantErr)
			}
			stats := ledger.snapshot()
			if stats.OpenSpoolFiles != 0 ||
				stats.CurrentSpoolBytes != 0 ||
				stats.LiveInlineBytes != 0 ||
				stats.EncoderMergeBytes != 0 {
				t.Fatalf("failed spool leaked resources: %+v", stats)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatalf("read spool directory: %v", readErr)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(sourcePath) {
				t.Fatalf("failed spool artifacts = %+v, want only source", entries)
			}
		})
	}
}

func TestMigrationValueStoreResourceMaximaAreDeterministic(t *testing.T) {
	run := func(t *testing.T) migrationResourceSnapshot {
		t.Helper()
		dir := t.TempDir()
		sourceBytes := bytes.Repeat([]byte("x"), migrationSourceBufferBytes+1)
		sourcePath := filepath.Join(dir, "source.json")
		if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
			t.Fatalf("write value source fixture: %v", err)
		}
		source, err := openRegularSessionFile(sourcePath, "value source fixture")
		if err != nil {
			t.Fatalf("open value source fixture: %v", err)
		}
		defer source.Close()
		ledger := newMigrationResourceLedger()
		store := newMigrationValueStoreWithInlineBudget(
			source,
			dir,
			ledger,
			osMigrationSpoolStorage{},
			0,
		)
		value, err := store.Retain(migrationJSONValueRange{
			Start: 0,
			End:   int64(len(sourceBytes)),
		})
		if err != nil {
			t.Fatalf("retain deterministic value: %v", err)
		}
		if err := value.CopyTo(io.Discard); err != nil {
			_ = value.Close()
			t.Fatalf("copy deterministic value: %v", err)
		}
		if err := value.Close(); err != nil {
			t.Fatalf("close deterministic value: %v", err)
		}
		return ledger.snapshot()
	}

	first := run(t)
	second := run(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("resource maxima changed across runs: first=%+v second=%+v", first, second)
	}
	if first.MaxEncoderMergeBytes != migrationCopyBufferBytes ||
		first.MaxOpenSpoolFiles != 1 ||
		first.PeakSpoolBytes != migrationSourceBufferBytes+1 ||
		first.OpenSpoolFiles != 0 ||
		first.CurrentSpoolBytes != 0 {
		t.Fatalf("deterministic resource snapshot = %+v", first)
	}
}

type failingMigrationSpoolStorage struct {
	delegate  migrationSpoolStorage
	createErr error
	writeErr  error
	syncErr   error
	closeErr  error
}

type readerFromMigrationDestination struct {
	bytes          []byte
	readFromCalled bool
}

func (d *readerFromMigrationDestination) Write(payload []byte) (int, error) {
	d.bytes = append(d.bytes, payload...)
	return len(payload), nil
}

func (d *readerFromMigrationDestination) ReadFrom(io.Reader) (int64, error) {
	d.readFromCalled = true
	return 0, errors.New("unexpected ReaderFrom delegation")
}

func (s *failingMigrationSpoolStorage) Create(dir string) (migrationSpoolWriter, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	writer, err := s.delegate.Create(dir)
	if err != nil {
		return nil, err
	}
	return &failingMigrationSpoolWriter{
		migrationSpoolWriter: writer,
		writeErr:             s.writeErr,
		syncErr:              s.syncErr,
		closeErr:             s.closeErr,
	}, nil
}

func (s *failingMigrationSpoolStorage) Open(path string) (migrationSpoolReader, error) {
	return s.delegate.Open(path)
}

func (s *failingMigrationSpoolStorage) Remove(path string) error {
	return s.delegate.Remove(path)
}

type failingMigrationSpoolWriter struct {
	migrationSpoolWriter
	writeErr error
	syncErr  error
	closeErr error
}

func (w *failingMigrationSpoolWriter) Write(payload []byte) (int, error) {
	if w.writeErr == nil {
		return w.migrationSpoolWriter.Write(payload)
	}
	return 0, w.writeErr
}

func (w *failingMigrationSpoolWriter) Sync() error {
	if w.syncErr != nil {
		return w.syncErr
	}
	return w.migrationSpoolWriter.Sync()
}

func (w *failingMigrationSpoolWriter) Close() error {
	closeErr := w.migrationSpoolWriter.Close()
	return errors.Join(closeErr, w.closeErr)
}
