package session

import (
	"crypto/sha256"
	"errors"
	"hash"
	"os"
	"path/filepath"
	"testing"
)

const migrationLargeFixtureBytes = 128 << 20

func TestMigrationValueLayerKeeps128MiBBulkValueBounded(t *testing.T) {
	runMigrationBulkFixture(t, migrationLargeFixtureBytes)
}

func TestMigrationValueLayerKeeps128MiBNonBulkArrayBounded(t *testing.T) {
	const elementBytes = 32 << 10
	const elementCount = migrationLargeFixtureBytes / elementBytes
	const contentBytes = elementBytes - 2

	dir := t.TempDir()
	path := filepath.Join(dir, "non-bulk.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create non-bulk fixture: %v", err)
	}
	expectedHash := sha256.New()
	if _, err := file.Write([]byte{'['}); err != nil {
		_ = file.Close()
		t.Fatalf("write non-bulk fixture prefix: %v", err)
	}
	content := make([]byte, contentBytes)
	for index := 0; index < elementCount; index++ {
		for byteIndex := range content {
			content[byteIndex] = byte('a' + index%26)
		}
		if index > 0 {
			if _, err := file.Write([]byte{','}); err != nil {
				_ = file.Close()
				t.Fatalf("write non-bulk fixture delimiter: %v", err)
			}
		}
		if err := writeHashedFixtureValue(file, expectedHash, content); err != nil {
			_ = file.Close()
			t.Fatalf("write non-bulk fixture element %d: %v", index, err)
		}
	}
	if _, err := file.Write([]byte{']'}); err != nil {
		_ = file.Close()
		t.Fatalf("write non-bulk fixture suffix: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close non-bulk fixture: %v", err)
	}

	source, err := openRegularSessionFile(path, "non-bulk fixture")
	if err != nil {
		t.Fatalf("open non-bulk fixture: %v", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatalf("stat non-bulk fixture: %v", err)
	}
	ledger := newMigrationResourceLedger()
	scanner, err := newMigrationJSONScanner(source, 0, info.Size(), ledger)
	if err != nil {
		t.Fatalf("create non-bulk scanner: %v", err)
	}
	defer scanner.Close()
	store := newMigrationValueStore(source, dir, ledger, osMigrationSpoolStorage{})
	actualHash := sha256.New()
	visited := 0
	if err := scanner.ScanArray(func(index int, valueRange migrationJSONValueRange) error {
		if index != visited {
			t.Fatalf("streamed element index = %d, want %d", index, visited)
		}
		if valueRange.Size() != elementBytes {
			t.Fatalf(
				"streamed element %d size = %d, want %d",
				index,
				valueRange.Size(),
				elementBytes,
			)
		}
		value, retainErr := store.Retain(valueRange)
		if retainErr != nil {
			return retainErr
		}
		copyErr := value.CopyTo(actualHash)
		closeErr := value.Close()
		visited++
		return errors.Join(copyErr, closeErr)
	}); err != nil {
		t.Fatalf("stream non-bulk fixture: %v", err)
	}
	if visited != elementCount {
		t.Fatalf("streamed element count = %d, want %d", visited, elementCount)
	}
	if expectedHashValue, actualHashValue := expectedHash.Sum(nil), actualHash.Sum(nil); !equalDigest(
		expectedHashValue,
		actualHashValue,
	) {
		t.Fatalf("non-bulk fixture value digest changed")
	}
	stats := ledger.snapshot()
	if stats.MaxLiveInlineBytes != elementBytes {
		t.Fatalf("maximum inline bytes = %d, want %d", stats.MaxLiveInlineBytes, elementBytes)
	}
	if stats.MaxSourceDecoderBytes != migrationSourceBufferBytes {
		t.Fatalf(
			"maximum decoder bytes = %d, want %d",
			stats.MaxSourceDecoderBytes,
			migrationSourceBufferBytes,
		)
	}
	if stats.MaxEncoderMergeBytes != 0 ||
		stats.MaxOpenSpoolFiles != 0 ||
		stats.PeakSpoolBytes != 0 ||
		stats.LiveInlineBytes != 0 ||
		stats.CurrentSpoolBytes != 0 {
		t.Fatalf("non-bulk resource stats = %+v", stats)
	}
}

func runMigrationBulkFixture(t *testing.T, contentBytes int64) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bulk.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create bulk fixture: %v", err)
	}
	const prefix = `{"payload":`
	if _, err := file.Write([]byte(prefix)); err != nil {
		_ = file.Close()
		t.Fatalf("write bulk fixture prefix: %v", err)
	}
	expectedHash := sha256.New()
	if _, err := file.Write([]byte{'"'}); err != nil {
		_ = file.Close()
		t.Fatalf("write bulk fixture opening quote: %v", err)
	}
	_, _ = expectedHash.Write([]byte{'"'})
	if err := writeRepeatedFixtureBytes(file, expectedHash, 'x', contentBytes); err != nil {
		_ = file.Close()
		t.Fatalf("write bulk fixture content: %v", err)
	}
	if _, err := file.Write([]byte{'"', '}'}); err != nil {
		_ = file.Close()
		t.Fatalf("write bulk fixture suffix: %v", err)
	}
	_, _ = expectedHash.Write([]byte{'"'})
	if err := file.Close(); err != nil {
		t.Fatalf("close bulk fixture: %v", err)
	}

	source, err := openRegularSessionFile(path, "bulk fixture")
	if err != nil {
		t.Fatalf("open bulk fixture: %v", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatalf("stat bulk fixture: %v", err)
	}
	sourceSize := info.Size()
	ledger := newMigrationResourceLedger()
	scanner, err := newMigrationJSONScanner(source, 0, sourceSize, ledger)
	if err != nil {
		t.Fatalf("create bulk scanner: %v", err)
	}
	scanned, err := scanner.ScanObject(migrationKnownFieldSet{"payload"})
	if err != nil {
		_ = scanner.Close()
		t.Fatalf("scan bulk fixture: %v", err)
	}
	if err := scanner.Close(); err != nil {
		t.Fatalf("close bulk scanner: %v", err)
	}
	valueRange, present := scanned.Value(0)
	if !present {
		t.Fatal("bulk payload field is absent")
	}
	if valueRange.Size() != contentBytes+2 {
		t.Fatalf("bulk payload size = %d, want %d", valueRange.Size(), contentBytes+2)
	}

	store := newMigrationValueStore(source, dir, ledger, osMigrationSpoolStorage{})
	value, err := store.Retain(valueRange)
	if err != nil {
		t.Fatalf("retain bulk migration value: %v", err)
	}
	if value.spoolPath == "" {
		t.Fatal("bulk migration value did not spool")
	}
	actualHash := sha256.New()
	if err := value.CopyTo(actualHash); err != nil {
		_ = value.Close()
		t.Fatalf("copy bulk migration value: %v", err)
	}
	if !equalDigest(expectedHash.Sum(nil), actualHash.Sum(nil)) {
		_ = value.Close()
		t.Fatal("bulk migration value digest changed")
	}
	stats := ledger.snapshot()
	if stats.MaxLiveInlineBytes != 0 ||
		stats.MaxSourceDecoderBytes != migrationSourceBufferBytes ||
		stats.MaxEncoderMergeBytes != migrationCopyBufferBytes ||
		stats.MaxOpenSpoolFiles != 1 ||
		stats.CurrentSpoolBytes != valueRange.Size() ||
		stats.PeakSpoolBytes != valueRange.Size() {
		_ = value.Close()
		t.Fatalf("bulk resource stats = %+v", stats)
	}
	if err := value.Close(); err != nil {
		t.Fatalf("close bulk migration value: %v", err)
	}
	stats = ledger.snapshot()
	if stats.LiveInlineBytes != 0 ||
		stats.SourceDecoderBytes != 0 ||
		stats.EncoderMergeBytes != 0 ||
		stats.OpenSpoolFiles != 0 ||
		stats.CurrentSpoolBytes != 0 {
		t.Fatalf("bulk resources remain after cleanup: %+v", stats)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bulk fixture after scan: %v", err)
	}
	if after.Size() != sourceSize {
		t.Fatalf("bulk source size changed: %d -> %d", sourceSize, after.Size())
	}
}

func writeHashedFixtureValue(file *os.File, digest hash.Hash, content []byte) error {
	if _, err := file.Write([]byte{'"'}); err != nil {
		return err
	}
	_, _ = digest.Write([]byte{'"'})
	if _, err := file.Write(content); err != nil {
		return err
	}
	_, _ = digest.Write(content)
	if _, err := file.Write([]byte{'"'}); err != nil {
		return err
	}
	_, _ = digest.Write([]byte{'"'})
	return nil
}

func writeRepeatedFixtureBytes(
	file *os.File,
	digest hash.Hash,
	value byte,
	size int64,
) error {
	buffer := make([]byte, migrationSourceBufferBytes)
	for index := range buffer {
		buffer[index] = value
	}
	for remaining := size; remaining > 0; {
		writeBytes := int64(len(buffer))
		if remaining < writeBytes {
			writeBytes = remaining
		}
		payload := buffer[:writeBytes]
		if _, err := file.Write(payload); err != nil {
			return err
		}
		_, _ = digest.Write(payload)
		remaining -= writeBytes
	}
	return nil
}

func equalDigest(left []byte, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
