package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationJSONScannerReturnsAbsoluteKnownFieldRange(t *testing.T) {
	const prefix = "ignored-prefix\n"
	const object = `{"unknown":{"nested":[1,2,3]},"payload": { "escaped" : "\u0061", "number" : 1.2300 }}`
	path := filepath.Join(t.TempDir(), "legacy-events.jsonl")
	if err := os.WriteFile(path, []byte(prefix+object+"\n"), 0o600); err != nil {
		t.Fatalf("write scanner fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "scanner fixture")
	if err != nil {
		t.Fatalf("open scanner fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	scanner, err := newMigrationJSONScanner(
		source,
		int64(len(prefix)),
		int64(len(prefix)+len(object)),
		ledger,
	)
	if err != nil {
		t.Fatalf("create migration JSON scanner: %v", err)
	}
	defer scanner.Close()

	scanned, err := scanner.ScanObject(migrationKnownFieldSet{"payload"})
	if err != nil {
		t.Fatalf("scan known object fields: %v", err)
	}
	valueRange, present := scanned.Value(0)
	if !present {
		t.Fatal("known payload field is absent")
	}
	want := []byte(`{ "escaped" : "\u0061", "number" : 1.2300 }`)
	got := make([]byte, valueRange.Size())
	if _, err := source.ReadAt(got, valueRange.Start); err != nil {
		t.Fatalf("read scanned value range: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("scanned range = %q, want %q", got, want)
	}
	if valueRange.Start <= int64(len(prefix)) || valueRange.End > int64(len(prefix)+len(object)) {
		t.Fatalf("scanned absolute range = [%d,%d)", valueRange.Start, valueRange.End)
	}
}

func TestMigrationJSONScannerStreamsArrayElementsAcrossBufferBoundaries(t *testing.T) {
	largeText := strings.Repeat("x", migrationSourceBufferBytes+17)
	document := `["` + largeText + `",{"nested":[true,false,null]},1.2300]`
	path := filepath.Join(t.TempDir(), "legacy-array.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write scanner fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "scanner fixture")
	if err != nil {
		t.Fatalf("open scanner fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	scanner, err := newMigrationJSONScanner(source, 0, int64(len(document)), ledger)
	if err != nil {
		t.Fatalf("create migration JSON scanner: %v", err)
	}
	defer scanner.Close()

	var ranges []migrationJSONValueRange
	if err := scanner.ScanArray(func(_ int, valueRange migrationJSONValueRange) error {
		ranges = append(ranges, valueRange)
		return nil
	}); err != nil {
		t.Fatalf("stream array elements: %v", err)
	}
	if len(ranges) != 3 {
		t.Fatalf("streamed element count = %d, want 3", len(ranges))
	}
	want := [][]byte{
		[]byte(`"` + largeText + `"`),
		[]byte(`{"nested":[true,false,null]}`),
		[]byte(`1.2300`),
	}
	for index, valueRange := range ranges {
		got := make([]byte, valueRange.Size())
		if _, err := source.ReadAt(got, valueRange.Start); err != nil {
			t.Fatalf("read streamed element %d: %v", index, err)
		}
		if !bytes.Equal(got, want[index]) {
			t.Fatalf("streamed element %d changed", index)
		}
	}
	stats := ledger.snapshot()
	if stats.MaxSourceDecoderBytes != migrationSourceBufferBytes {
		t.Fatalf(
			"maximum source decoder bytes = %d, want %d",
			stats.MaxSourceDecoderBytes,
			migrationSourceBufferBytes,
		)
	}
}

func TestMigrationJSONScannerEnforcesStructureAndNestingCeiling(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{
			name: "maximum nesting",
			value: strings.Repeat("[", migrationMaxJSONNesting) +
				"0" +
				strings.Repeat("]", migrationMaxJSONNesting),
		},
		{
			name: "excessive nesting",
			value: strings.Repeat("[", migrationMaxJSONNesting+1) +
				"0" +
				strings.Repeat("]", migrationMaxJSONNesting+1),
			wantErr: errMigrationJSONComplex,
		},
		{
			name:    "truncated string",
			value:   `{"payload":"unterminated}`,
			wantErr: errMigrationJSONMalformed,
		},
		{
			name:    "invalid number",
			value:   `{"payload":01}`,
			wantErr: errMigrationJSONMalformed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy-value.json")
			if err := os.WriteFile(path, []byte(test.value), 0o600); err != nil {
				t.Fatalf("write scanner fixture: %v", err)
			}
			source, err := openRegularSessionFile(path, "scanner fixture")
			if err != nil {
				t.Fatalf("open scanner fixture: %v", err)
			}
			defer source.Close()
			scanner, err := newMigrationJSONScanner(
				source,
				0,
				int64(len(test.value)),
				newMigrationResourceLedger(),
			)
			if err != nil {
				t.Fatalf("create migration JSON scanner: %v", err)
			}
			defer scanner.Close()

			if strings.HasPrefix(test.value, "[") {
				err = scanner.ScanArray(func(_ int, _ migrationJSONValueRange) error {
					return nil
				})
			} else {
				_, err = scanner.ScanObject(migrationKnownFieldSet{"payload"})
			}
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("scan maximum valid structure: %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %T %v, want %v", err, err, test.wantErr)
			}
		})
	}
}

func TestMigrationJSONScannerClassifiesTruncatedStructureAsMalformed(t *testing.T) {
	for _, document := range []string{
		`{"payload":`,
		`{"payload":true`,
		`[`,
		`{"payload":{`,
	} {
		t.Run(document, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "truncated.json")
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatalf("write truncated fixture: %v", err)
			}
			source, err := openRegularSessionFile(path, "truncated fixture")
			if err != nil {
				t.Fatalf("open truncated fixture: %v", err)
			}
			defer source.Close()
			scanner, err := newMigrationJSONScanner(
				source,
				0,
				int64(len(document)),
				newMigrationResourceLedger(),
			)
			if err != nil {
				t.Fatalf("create truncated scanner: %v", err)
			}
			defer scanner.Close()
			if strings.HasPrefix(document, "[") {
				err = scanner.ScanArray(func(_ int, _ migrationJSONValueRange) error { return nil })
			} else {
				_, err = scanner.ScanObject(migrationKnownFieldSet{"payload"})
			}
			if !errors.Is(err, errMigrationJSONMalformed) {
				t.Fatalf("error = %T %v, want malformed JSON", err, err)
			}
		})
	}
}

func TestMigrationJSONScannerPreservesEncodingJSONInvalidUTF8CompatibilityAndRejectsUseAfterClose(t *testing.T) {
	document := []byte{'{', '"', 'p', 'a', 'y', 'l', 'o', 'a', 'd', '"', ':', '"', 0xff, '"', '}'}
	path := filepath.Join(t.TempDir(), "invalid-utf8.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatalf("write invalid UTF-8 fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "invalid UTF-8 fixture")
	if err != nil {
		t.Fatalf("open invalid UTF-8 fixture: %v", err)
	}
	defer source.Close()
	scanner, err := newMigrationJSONScanner(
		source,
		0,
		int64(len(document)),
		newMigrationResourceLedger(),
	)
	if err != nil {
		t.Fatalf("create invalid UTF-8 scanner: %v", err)
	}
	scanned, err := scanner.ScanObject(migrationKnownFieldSet{"payload"})
	if err != nil {
		t.Fatalf("scan encoding/json-compatible invalid UTF-8: %v", err)
	}
	valueRange, present := scanned.Value(0)
	if !present {
		t.Fatal("invalid UTF-8 payload range is absent")
	}
	got := make([]byte, valueRange.Size())
	if _, err := source.ReadAt(got, valueRange.Start); err != nil {
		t.Fatalf("read invalid UTF-8 payload range: %v", err)
	}
	if !bytes.Equal(got, []byte{'"', 0xff, '"'}) {
		t.Fatalf("invalid UTF-8 payload range = %v", got)
	}
	if err := scanner.Close(); err != nil {
		t.Fatalf("close scanner: %v", err)
	}
	if _, err := scanner.ScanObject(migrationKnownFieldSet{"payload"}); !errors.Is(err, errMigrationJSONScannerClosed) {
		t.Fatalf("closed scanner error = %T %v, want scanner closed", err, err)
	}
}

func TestMigrationJSONScannerUsesAbsoluteRangesAcrossNonzeroRefillBoundary(t *testing.T) {
	prefix := strings.Repeat("p", migrationSourceBufferBytes-6)
	document := `{"payload":{"text":"crosses-buffer-boundary"}}`
	path := filepath.Join(t.TempDir(), "offset.json")
	if err := os.WriteFile(path, []byte(prefix+document), 0o600); err != nil {
		t.Fatalf("write offset fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "offset fixture")
	if err != nil {
		t.Fatalf("open offset fixture: %v", err)
	}
	defer source.Close()
	scanner, err := newMigrationJSONScanner(
		source,
		int64(len(prefix)),
		int64(len(prefix)+len(document)),
		newMigrationResourceLedger(),
	)
	if err != nil {
		t.Fatalf("create offset scanner: %v", err)
	}
	defer scanner.Close()
	scanned, err := scanner.ScanObject(migrationKnownFieldSet{"payload"})
	if err != nil {
		t.Fatalf("scan offset fixture: %v", err)
	}
	valueRange, present := scanned.Value(0)
	if !present {
		t.Fatal("payload range is absent")
	}
	want := []byte(`{"text":"crosses-buffer-boundary"}`)
	got := make([]byte, valueRange.Size())
	if _, err := source.ReadAt(got, valueRange.Start); err != nil {
		t.Fatalf("read offset range: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("offset range = %q, want %q", got, want)
	}
}

func TestMigrationJSONScannerKnownFieldDuplicatesUseLastCompatibleValue(t *testing.T) {
	document := `{"payload":1,"\u0070ayload":2}`
	path := filepath.Join(t.TempDir(), "duplicate.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write duplicate fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "duplicate fixture")
	if err != nil {
		t.Fatalf("open duplicate fixture: %v", err)
	}
	defer source.Close()
	scanner, err := newMigrationJSONScanner(
		source,
		0,
		int64(len(document)),
		newMigrationResourceLedger(),
	)
	if err != nil {
		t.Fatalf("create duplicate scanner: %v", err)
	}
	defer scanner.Close()
	scanned, err := scanner.ScanObject(migrationKnownFieldSet{"payload"})
	if err != nil {
		t.Fatalf("scan duplicate fixture: %v", err)
	}
	valueRange, present := scanned.Value(0)
	if !present {
		t.Fatal("duplicate payload range is absent")
	}
	got := make([]byte, valueRange.Size())
	if _, err := source.ReadAt(got, valueRange.Start); err != nil {
		t.Fatalf("read duplicate range: %v", err)
	}
	if !bytes.Equal(got, []byte("2")) {
		t.Fatalf("duplicate payload range = %q, want last value 2", got)
	}
}

func TestMigrationJSONScannerArrayVisitorFailureStopsTraversal(t *testing.T) {
	document := `[0,1,2]`
	path := filepath.Join(t.TempDir(), "visitor.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write visitor fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "visitor fixture")
	if err != nil {
		t.Fatalf("open visitor fixture: %v", err)
	}
	defer source.Close()
	scanner, err := newMigrationJSONScanner(
		source,
		0,
		int64(len(document)),
		newMigrationResourceLedger(),
	)
	if err != nil {
		t.Fatalf("create visitor scanner: %v", err)
	}
	defer scanner.Close()
	wantErr := errors.New("stop array traversal")
	visits := 0
	err = scanner.ScanArray(func(index int, _ migrationJSONValueRange) error {
		visits++
		if index != 0 {
			t.Fatalf("visited index %d after callback failure", index)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("visitor error = %T %v, want %v", err, err, wantErr)
	}
	if visits != 1 {
		t.Fatalf("visitor calls = %d, want 1", visits)
	}
}
