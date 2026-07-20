package session

import (
	"errors"
	"io"

	"core/shared/boundedjson"
)

var (
	errMigrationJSONMalformed     = boundedjson.ErrMalformed
	errMigrationJSONComplex       = boundedjson.ErrComplex
	errMigrationJSONScannerClosed = boundedjson.ErrClosed
)

type migrationJSONValueRange = boundedjson.Range
type migrationKnownFieldSet = boundedjson.KnownFields
type migrationScannedObject = boundedjson.ScannedObject

type migrationJSONScanner struct {
	*boundedjson.Scanner
	releaseDecoder func()
}

func newMigrationJSONScanner(
	source io.ReaderAt,
	start int64,
	end int64,
	ledger *migrationResourceLedger,
) (*migrationJSONScanner, error) {
	release, err := ledger.acquireSourceDecoder(migrationSourceBufferBytes)
	if err != nil {
		return nil, err
	}
	scanner, err := boundedjson.NewScanner(
		source,
		start,
		end,
		make([]byte, migrationSourceBufferBytes),
		false,
	)
	if err != nil {
		release()
		return nil, err
	}
	return &migrationJSONScanner{
		Scanner:        scanner,
		releaseDecoder: release,
	}, nil
}

func (s *migrationJSONScanner) Close() error {
	if s == nil {
		return nil
	}
	err := s.Scanner.Close()
	if s.releaseDecoder != nil {
		s.releaseDecoder()
		s.releaseDecoder = nil
	}
	return errors.Join(err)
}
