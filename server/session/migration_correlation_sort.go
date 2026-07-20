package session

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
)

const (
	migrationCorrelationRunBudgetBytes = 8 << 20
	migrationCorrelationMergeFanIn     = 8
	migrationCorrelationBufferBytes    = 64 << 10
)

const (
	migrationCorrelationCallSource byte = iota + 1
	migrationCorrelationQuerySource
	migrationCorrelationResolutionSource
)

type migrationCorrelationCallDefinition struct {
	NormalizedCallID []byte
	Sequence         int64
	Ordinal          int64
	Custom           bool
	Name             string
}

type migrationCorrelationCompletionQuery struct {
	NormalizedCallID []byte
	Sequence         int64
	Ordinal          int64
	Name             string
}

type migrationCorrelationResolution struct {
	Sequence int64
	Ordinal  int64
	Custom   bool
	Name     string
}

type migrationCorrelationRecordKind byte

const (
	migrationCorrelationCallRecord migrationCorrelationRecordKind = iota + 1
	migrationCorrelationQueryRecord
	migrationCorrelationResolutionRecord
)

type migrationCorrelationArtifact struct {
	path    string
	size    int64
	writer  *migrationCorrelationArtifactWriter
	removed bool
	next    *migrationCorrelationArtifact
}

type migrationCorrelationArtifactWriter struct {
	sorter   *migrationCorrelationSorter
	artifact *migrationCorrelationArtifact
	writer   migrationSpoolWriter
	buffer   *bufio.Writer
	release  func()
	closed   bool
}

type migrationCorrelationSource struct {
	tag   byte
	data  *migrationCorrelationArtifactWriter
	index *migrationCorrelationArtifactWriter
}

type migrationCorrelationReference struct {
	source byte
	offset int64
	size   int64
}

// migrationCorrelationSorter owns exactly three sequential tuple sources and
// bounded run inventories. It never creates an artifact for an individual tuple.
type migrationCorrelationSorter struct {
	ctx      context.Context
	spoolDir string
	ledger   *migrationResourceLedger
	storage  migrationSpoolStorage

	calls       migrationCorrelationSource
	queries     migrationCorrelationSource
	resolutions migrationCorrelationSource

	manualArtifacts *migrationCorrelationArtifact
	activeInventory *migrationCorrelationArtifact
	nextInventory   *migrationCorrelationArtifact
	artifactCount   int
	created         int
	finished        bool
	closed          bool
}

type migrationCorrelationResolutionStream struct {
	sorter      *migrationCorrelationSorter
	run         *migrationCorrelationArtifact
	reader      *migrationCorrelationReferenceReader
	lastSeq     int64
	lastOrdinal int64
	hasLast     bool
	closed      bool
}

func newMigrationCorrelationSorter(
	ctx context.Context,
	spoolDir string,
	ledger *migrationResourceLedger,
	storage migrationSpoolStorage,
) (_ *migrationCorrelationSorter, resultErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("migration correlation context is required")
	}
	if spoolDir == "" {
		return nil, fmt.Errorf("migration correlation artifact directory is required")
	}
	if ledger == nil {
		return nil, fmt.Errorf("migration correlation resource ledger is required")
	}
	if storage == nil {
		return nil, fmt.Errorf("migration correlation artifact storage is required")
	}
	sorter := &migrationCorrelationSorter{
		ctx: ctx, spoolDir: spoolDir, ledger: ledger, storage: storage,
	}
	if sorter.calls, resultErr = sorter.newSource(migrationCorrelationCallSource); resultErr != nil {
		return nil, resultErr
	}
	if sorter.queries, resultErr = sorter.newSource(migrationCorrelationQuerySource); resultErr != nil {
		return nil, errors.Join(resultErr, sorter.Close())
	}
	return sorter, nil
}

func (s *migrationCorrelationSorter) AddCall(call migrationCorrelationCallDefinition) error {
	if err := s.checkAdding(); err != nil {
		return err
	}
	if err := validateMigrationCorrelationKey(call.NormalizedCallID, call.Sequence, call.Ordinal); err != nil {
		return fmt.Errorf("add migration correlation call: %w", err)
	}
	return s.appendRecord(
		&s.calls,
		migrationCorrelationCallRecord,
		call.NormalizedCallID,
		call.Sequence,
		call.Ordinal,
		call.Custom,
		call.Name,
	)
}

func (s *migrationCorrelationSorter) AddQuery(query migrationCorrelationCompletionQuery) error {
	if err := s.checkAdding(); err != nil {
		return err
	}
	if err := validateMigrationCorrelationKey(query.NormalizedCallID, query.Sequence, query.Ordinal); err != nil {
		return fmt.Errorf("add migration correlation completion query: %w", err)
	}
	return s.appendRecord(
		&s.queries,
		migrationCorrelationQueryRecord,
		query.NormalizedCallID,
		query.Sequence,
		query.Ordinal,
		false,
		query.Name,
	)
}

func (s *migrationCorrelationSorter) Finish() (_ *migrationCorrelationResolutionStream, resultErr error) {
	if s == nil {
		return nil, fmt.Errorf("migration correlation sorter is required")
	}
	if s.closed {
		return nil, fmt.Errorf("migration correlation sorter is closed")
	}
	if s.finished {
		return nil, fmt.Errorf("migration correlation sorter is already finished")
	}
	s.finished = true
	if err := s.ctx.Err(); err != nil {
		return nil, s.fail(err)
	}
	if err := s.closeSource(&s.calls); err != nil {
		return nil, s.fail(err)
	}
	if err := s.closeSource(&s.queries); err != nil {
		return nil, s.fail(err)
	}
	firstRun, err := s.sortSources(
		[]*migrationCorrelationSource{&s.calls, &s.queries},
		migrationCorrelationCompareByCall,
	)
	if err != nil {
		return nil, s.fail(err)
	}
	if s.resolutions, err = s.newSource(migrationCorrelationResolutionSource); err != nil {
		return nil, s.fail(errors.Join(err, s.removeArtifact(firstRun)))
	}
	if err := s.resolveFirstRun(firstRun); err != nil {
		return nil, s.fail(err)
	}
	if err := s.closeSource(&s.calls); err != nil {
		return nil, s.fail(err)
	}
	if err := s.closeSource(&s.queries); err != nil {
		return nil, s.fail(err)
	}
	if err := s.removeSource(&s.calls); err != nil {
		return nil, s.fail(err)
	}
	if err := s.removeSource(&s.queries); err != nil {
		return nil, s.fail(err)
	}
	if err := s.closeSource(&s.resolutions); err != nil {
		return nil, s.fail(err)
	}
	secondRun, err := s.sortSources(
		[]*migrationCorrelationSource{&s.resolutions},
		migrationCorrelationCompareByResolution,
	)
	if err != nil {
		return nil, s.fail(err)
	}
	reader, err := s.openReferenceReader(secondRun)
	if err != nil {
		return nil, s.fail(errors.Join(err, s.removeArtifact(secondRun)))
	}
	return &migrationCorrelationResolutionStream{
		sorter: s, run: secondRun, reader: reader,
	}, nil
}

func (s *migrationCorrelationSorter) ArtifactCount() int {
	if s == nil {
		return 0
	}
	return s.artifactCount
}

func (s *migrationCorrelationSorter) CreatedArtifactCount() int {
	if s == nil {
		return 0
	}
	return s.created
}

func (s *migrationCorrelationSorter) Close() error {
	if s == nil {
		return nil
	}
	s.closed = true
	var errs []error
	errs = append(errs, s.closeSource(&s.calls), s.closeSource(&s.queries), s.closeSource(&s.resolutions))
	errs = append(errs, s.cleanupInventory(&s.nextInventory), s.cleanupInventory(&s.activeInventory))
	errs = append(errs, s.removeSource(&s.calls), s.removeSource(&s.queries), s.removeSource(&s.resolutions))
	for artifact := s.manualArtifacts; artifact != nil; artifact = artifact.next {
		errs = append(errs, s.removeArtifact(artifact))
	}
	return errors.Join(errs...)
}

func (s *migrationCorrelationSorter) fail(err error) error {
	return errors.Join(err, s.Close())
}

func (s *migrationCorrelationSorter) checkAdding() error {
	if s == nil {
		return fmt.Errorf("migration correlation sorter is required")
	}
	if s.closed {
		return fmt.Errorf("migration correlation sorter is closed")
	}
	if s.finished {
		return fmt.Errorf("migration correlation sorter has already finished")
	}
	return s.ctx.Err()
}

func (stream *migrationCorrelationResolutionStream) Next() (
	migrationCorrelationResolution,
	bool,
	error,
) {
	if stream == nil {
		return migrationCorrelationResolution{}, false, fmt.Errorf("migration correlation resolution stream is required")
	}
	if stream.closed {
		return migrationCorrelationResolution{}, false, fmt.Errorf("migration correlation resolution stream is closed")
	}
	if err := stream.sorter.ctx.Err(); err != nil {
		return migrationCorrelationResolution{}, false, stream.fail(err)
	}
	ref, found, err := stream.reader.Next()
	if err != nil {
		return migrationCorrelationResolution{}, false, stream.fail(err)
	}
	if !found {
		if err := stream.Close(); err != nil {
			return migrationCorrelationResolution{}, false, err
		}
		return migrationCorrelationResolution{}, false, nil
	}
	record, err := stream.sorter.readResolution(ref)
	if err != nil {
		return migrationCorrelationResolution{}, false, stream.fail(err)
	}
	if stream.hasLast && record.Sequence == stream.lastSeq && record.Ordinal == stream.lastOrdinal {
		return migrationCorrelationResolution{}, false, stream.fail(fmt.Errorf(
			"migration correlation contains duplicate completion key: sequence=%d ordinal=%d",
			record.Sequence,
			record.Ordinal,
		))
	}
	stream.lastSeq, stream.lastOrdinal, stream.hasLast = record.Sequence, record.Ordinal, true
	return record, true, nil
}

func (stream *migrationCorrelationResolutionStream) fail(err error) error {
	return errors.Join(err, stream.Close())
}

func (stream *migrationCorrelationResolutionStream) Close() error {
	if stream == nil || stream.closed {
		return nil
	}
	stream.closed = true
	return errors.Join(
		stream.reader.Close(),
		stream.sorter.removeArtifact(stream.run),
		stream.sorter.removeSource(&stream.sorter.resolutions),
		stream.sorter.Close(),
	)
}

func validateMigrationCorrelationKey(id []byte, sequence, ordinal int64) error {
	if len(id) == 0 {
		return fmt.Errorf("normalized call ID is required")
	}
	if sequence <= 0 {
		return fmt.Errorf("sequence must be positive: %d", sequence)
	}
	if ordinal < 0 {
		return fmt.Errorf("ordinal must not be negative: %d", ordinal)
	}
	return nil
}

func (s *migrationCorrelationSorter) newSource(tag byte) (migrationCorrelationSource, error) {
	data, err := s.newOwnedArtifactWriter()
	if err != nil {
		return migrationCorrelationSource{}, err
	}
	index, err := s.newOwnedArtifactWriter()
	if err != nil {
		return migrationCorrelationSource{}, errors.Join(err, s.removeArtifact(data.artifact))
	}
	return migrationCorrelationSource{tag: tag, data: data, index: index}, nil
}

// newArtifactWriter is retained for the epoch descriptor owned by
// migration_legacy_transform. It is bounded external ownership, not tuple storage.
func (s *migrationCorrelationSorter) newArtifactWriter() (*migrationCorrelationArtifactWriter, error) {
	writer, err := s.newOwnedArtifactWriter()
	if err != nil {
		return nil, err
	}
	writer.artifact.next = s.manualArtifacts
	s.manualArtifacts = writer.artifact
	return writer, nil
}

func (s *migrationCorrelationSorter) newOwnedArtifactWriter() (*migrationCorrelationArtifactWriter, error) {
	releaseFile, err := s.ledger.acquireSpoolFile()
	if err != nil {
		return nil, err
	}
	writer, err := s.storage.Create(s.spoolDir)
	if err != nil {
		releaseFile()
		return nil, fmt.Errorf("create migration correlation artifact: %w", err)
	}
	releaseBuffer, err := s.ledger.acquireEncoderMerge(migrationCorrelationBufferBytes)
	if err != nil {
		closeErr := writer.Close()
		removeErr := s.storage.Remove(writer.Name())
		releaseFile()
		if removeErr != nil {
			artifact := &migrationCorrelationArtifact{path: writer.Name()}
			artifact.next = s.manualArtifacts
			s.manualArtifacts = artifact
			s.artifactCount++
			s.created++
		}
		return nil, errors.Join(
			fmt.Errorf("lease migration correlation artifact buffer: %w", err),
			closeErr,
			removeErr,
		)
	}
	artifact := &migrationCorrelationArtifact{path: writer.Name()}
	result := &migrationCorrelationArtifactWriter{
		sorter: s, artifact: artifact, writer: writer,
		buffer: bufio.NewWriterSize(writer, migrationCorrelationBufferBytes),
		release: func() {
			releaseBuffer()
			releaseFile()
		},
	}
	if err := writer.Chmod(0o600); err != nil {
		closeErr := result.Close()
		removeErr := s.storage.Remove(artifact.path)
		return nil, errors.Join(
			fmt.Errorf("set migration correlation artifact mode: %w", err),
			closeErr,
			removeErr,
		)
	}
	artifact.writer = result
	s.artifactCount++
	s.created++
	return result, nil
}

func (w *migrationCorrelationArtifactWriter) Write(payload []byte) (int, error) {
	if w == nil || w.closed {
		return 0, fmt.Errorf("migration correlation artifact writer is closed")
	}
	n, err := w.buffer.Write(payload)
	if n > 0 {
		w.artifact.size += int64(n)
		if ledgerErr := w.sorter.ledger.spoolGrew(int64(n)); ledgerErr != nil {
			return n, errors.Join(err, ledgerErr)
		}
	}
	return n, err
}

func (w *migrationCorrelationArtifactWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	err := errors.Join(
		wrapMigrationError("flush migration correlation artifact", w.buffer.Flush()),
		wrapMigrationError("sync migration correlation artifact", w.writer.Sync()),
		wrapMigrationError("close migration correlation artifact", w.writer.Close()),
	)
	w.release()
	return err
}

func (s *migrationCorrelationSorter) closeSource(source *migrationCorrelationSource) error {
	if source == nil {
		return nil
	}
	var errs []error
	if source.data != nil {
		errs = append(errs, source.data.Close())
	}
	if source.index != nil {
		errs = append(errs, source.index.Close())
	}
	return errors.Join(errs...)
}

func (s *migrationCorrelationSorter) removeSource(source *migrationCorrelationSource) error {
	if source == nil {
		return nil
	}
	var errs []error
	if source.data != nil {
		errs = append(errs, s.removeArtifact(source.data.artifact))
	}
	if source.index != nil {
		errs = append(errs, s.removeArtifact(source.index.artifact))
	}
	return errors.Join(errs...)
}

func (s *migrationCorrelationSorter) removeArtifact(artifact *migrationCorrelationArtifact) error {
	if artifact == nil || artifact.removed {
		return nil
	}
	closeErr := error(nil)
	if artifact.writer != nil {
		closeErr = artifact.writer.Close()
	}
	if err := s.storage.Remove(artifact.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if ledgerErr := s.ledger.spoolRemoved(artifact.size); ledgerErr != nil {
				return errors.Join(closeErr, ledgerErr)
			}
			artifact.removed = true
			s.artifactCount--
			return closeErr
		}
		return errors.Join(closeErr, fmt.Errorf("remove migration correlation artifact: %w", err))
	}
	if err := s.ledger.spoolRemoved(artifact.size); err != nil {
		return errors.Join(closeErr, err)
	}
	artifact.removed = true
	s.artifactCount--
	return closeErr
}

func (s *migrationCorrelationSorter) appendRecord(
	source *migrationCorrelationSource,
	kind migrationCorrelationRecordKind,
	id []byte,
	sequence, ordinal int64,
	custom bool,
	name string,
) error {
	if source == nil || source.data == nil || source.index == nil {
		return fmt.Errorf("migration correlation source is unavailable")
	}
	offset := source.data.artifact.size
	if err := writeMigrationCorrelationRecord(source.data, kind, id, sequence, ordinal, custom, name); err != nil {
		return err
	}
	return writeMigrationCorrelationReference(source.index, migrationCorrelationReference{
		source: source.tag, offset: offset, size: source.data.artifact.size - offset,
	})
}

func writeMigrationCorrelationRecord(
	writer io.Writer,
	kind migrationCorrelationRecordKind,
	id []byte,
	sequence, ordinal int64,
	custom bool,
	name string,
) error {
	return writeMigrationCorrelationRecordPrefix(
		writer, kind, uint64(len(id)), sequence, ordinal, custom, uint64(len(name)),
		func() error {
			if err := writeMigrationBytes(writer, id); err != nil {
				return err
			}
			return writeMigrationString(writer, name)
		},
	)
}

func writeMigrationCorrelationRecordPrefix(
	writer io.Writer,
	kind migrationCorrelationRecordKind,
	idLength uint64,
	sequence, ordinal int64,
	custom bool,
	nameLength uint64,
	body func() error,
) error {
	if err := writeMigrationBytes(writer, []byte{byte(kind)}); err != nil {
		return err
	}
	for _, value := range []uint64{idLength, uint64(sequence), uint64(ordinal)} {
		if err := writeMigrationCorrelationUvarint(writer, value); err != nil {
			return err
		}
	}
	flag := byte(0)
	if custom {
		flag = 1
	}
	if err := writeMigrationBytes(writer, []byte{flag}); err != nil {
		return err
	}
	if err := writeMigrationCorrelationUvarint(writer, nameLength); err != nil {
		return err
	}
	return body()
}

func writeMigrationCorrelationUvarint(writer io.Writer, value uint64) error {
	var encoded [binary.MaxVarintLen64]byte
	return writeMigrationBytes(writer, encoded[:binary.PutUvarint(encoded[:], value)])
}

func writeMigrationString(writer io.Writer, value string) error {
	for len(value) > 0 {
		n, err := io.WriteString(writer, value)
		if n > 0 {
			value = value[n:]
		}
		if err != nil {
			return fmt.Errorf("write migration correlation string: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("write migration correlation string: %w", io.ErrNoProgress)
		}
	}
	return nil
}

type migrationCorrelationRecordHeader struct {
	kind       migrationCorrelationRecordKind
	idLength   uint64
	sequence   int64
	ordinal    int64
	custom     bool
	nameLength uint64
}

type migrationCorrelationArtifactReader struct {
	reader  io.ReadCloser
	buffer  *bufio.Reader
	release func()
	closed  bool
}

func (s *migrationCorrelationSorter) openArtifactReader(
	artifact *migrationCorrelationArtifact,
) (*migrationCorrelationArtifactReader, error) {
	return s.openArtifactReaderAt(artifact, 0)
}

func (s *migrationCorrelationSorter) openArtifactReaderAt(
	artifact *migrationCorrelationArtifact,
	offset int64,
) (*migrationCorrelationArtifactReader, error) {
	if artifact == nil || artifact.removed {
		return nil, fmt.Errorf("migration correlation artifact is unavailable")
	}
	releaseFile, err := s.ledger.acquireSpoolFile()
	if err != nil {
		return nil, err
	}
	reader, err := s.storage.Open(artifact.path)
	if err != nil {
		releaseFile()
		return nil, fmt.Errorf("open migration correlation artifact: %w", err)
	}
	seeker, ok := reader.(io.Seeker)
	if !ok {
		closeErr := reader.Close()
		releaseFile()
		return nil, errors.Join(closeErr, fmt.Errorf("migration correlation artifact reader is not seekable"))
	}
	if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
		closeErr := reader.Close()
		releaseFile()
		return nil, errors.Join(closeErr, fmt.Errorf("seek migration correlation artifact: %w", err))
	}
	releaseBuffer, err := s.ledger.acquireEncoderMerge(migrationCorrelationBufferBytes)
	if err != nil {
		closeErr := reader.Close()
		releaseFile()
		return nil, errors.Join(closeErr, err)
	}
	return &migrationCorrelationArtifactReader{
		reader: reader, buffer: bufio.NewReaderSize(reader, migrationCorrelationBufferBytes),
		release: func() { releaseBuffer(); releaseFile() },
	}, nil
}

func (r *migrationCorrelationArtifactReader) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	err := r.reader.Close()
	r.release()
	return err
}

func (r *migrationCorrelationArtifactReader) readHeader() (migrationCorrelationRecordHeader, error) {
	kind, err := r.buffer.ReadByte()
	if err != nil {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("read migration correlation record kind: %w", err)
	}
	if migrationCorrelationRecordKind(kind) < migrationCorrelationCallRecord ||
		migrationCorrelationRecordKind(kind) > migrationCorrelationResolutionRecord {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("invalid migration correlation record kind: %d", kind)
	}
	idLength, err := binary.ReadUvarint(r.buffer)
	if err != nil {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("read migration correlation ID length: %w", err)
	}
	sequence, err := binary.ReadUvarint(r.buffer)
	if err != nil || sequence > uint64(^uint64(0)>>1) {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("read migration correlation sequence: %w", err)
	}
	ordinal, err := binary.ReadUvarint(r.buffer)
	if err != nil || ordinal > uint64(^uint64(0)>>1) {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("read migration correlation ordinal: %w", err)
	}
	custom, err := r.buffer.ReadByte()
	if err != nil || custom > 1 {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("read migration correlation custom discriminator: %w", err)
	}
	nameLength, err := binary.ReadUvarint(r.buffer)
	if err != nil {
		return migrationCorrelationRecordHeader{}, fmt.Errorf("read migration correlation name length: %w", err)
	}
	return migrationCorrelationRecordHeader{
		kind: migrationCorrelationRecordKind(kind), idLength: idLength,
		sequence: int64(sequence), ordinal: int64(ordinal), custom: custom == 1,
		nameLength: nameLength,
	}, nil
}

func writeMigrationCorrelationReference(
	writer io.Writer,
	ref migrationCorrelationReference,
) error {
	if ref.source < migrationCorrelationCallSource || ref.source > migrationCorrelationResolutionSource ||
		ref.offset < 0 || ref.size <= 0 {
		return fmt.Errorf("invalid migration correlation reference")
	}
	if err := writeMigrationBytes(writer, []byte{ref.source}); err != nil {
		return err
	}
	for _, value := range []uint64{uint64(ref.offset), uint64(ref.size)} {
		if err := writeMigrationCorrelationUvarint(writer, value); err != nil {
			return err
		}
	}
	return nil
}

type migrationCorrelationReferenceReader struct {
	reader *migrationCorrelationArtifactReader
}

func (s *migrationCorrelationSorter) openReferenceReader(
	artifact *migrationCorrelationArtifact,
) (*migrationCorrelationReferenceReader, error) {
	reader, err := s.openArtifactReader(artifact)
	if err != nil {
		return nil, err
	}
	return &migrationCorrelationReferenceReader{reader: reader}, nil
}

func (r *migrationCorrelationReferenceReader) Next() (migrationCorrelationReference, bool, error) {
	if r == nil || r.reader == nil || r.reader.closed {
		return migrationCorrelationReference{}, false, fmt.Errorf("migration correlation reference reader is closed")
	}
	source, err := r.reader.buffer.ReadByte()
	if errors.Is(err, io.EOF) {
		return migrationCorrelationReference{}, false, nil
	}
	if err != nil {
		return migrationCorrelationReference{}, false, fmt.Errorf("read migration correlation reference source: %w", err)
	}
	offset, err := binary.ReadUvarint(r.reader.buffer)
	if err != nil || offset > uint64(^uint64(0)>>1) {
		return migrationCorrelationReference{}, false, fmt.Errorf("read migration correlation reference offset: %w", err)
	}
	size, err := binary.ReadUvarint(r.reader.buffer)
	if err != nil || size > uint64(^uint64(0)>>1) || size == 0 {
		return migrationCorrelationReference{}, false, fmt.Errorf("read migration correlation reference size: %w", err)
	}
	ref := migrationCorrelationReference{source: source, offset: int64(offset), size: int64(size)}
	if ref.source < migrationCorrelationCallSource || ref.source > migrationCorrelationResolutionSource {
		return migrationCorrelationReference{}, false, fmt.Errorf("invalid migration correlation reference source: %d", source)
	}
	return ref, true, nil
}

func (r *migrationCorrelationReferenceReader) Close() error {
	if r == nil || r.reader == nil {
		return nil
	}
	return r.reader.Close()
}

func (s *migrationCorrelationSorter) sourceFor(tag byte) (*migrationCorrelationSource, error) {
	switch tag {
	case migrationCorrelationCallSource:
		return &s.calls, nil
	case migrationCorrelationQuerySource:
		return &s.queries, nil
	case migrationCorrelationResolutionSource:
		return &s.resolutions, nil
	default:
		return nil, fmt.Errorf("unknown migration correlation source: %d", tag)
	}
}

func (s *migrationCorrelationSorter) openRecord(
	ref migrationCorrelationReference,
) (*migrationCorrelationArtifactReader, migrationCorrelationRecordHeader, error) {
	source, err := s.sourceFor(ref.source)
	if err != nil || source.data == nil {
		return nil, migrationCorrelationRecordHeader{}, errors.Join(err, fmt.Errorf("migration correlation source is unavailable"))
	}
	reader, err := s.openArtifactReaderAt(source.data.artifact, ref.offset)
	if err != nil {
		return nil, migrationCorrelationRecordHeader{}, err
	}
	header, err := reader.readHeader()
	if err != nil {
		return nil, migrationCorrelationRecordHeader{}, errors.Join(err, reader.Close())
	}
	return reader, header, nil
}

type migrationCorrelationCompare func(
	*migrationCorrelationSorter,
	migrationCorrelationReference,
	migrationCorrelationReference,
) (int, error)

func migrationCorrelationCompareByCall(
	s *migrationCorrelationSorter,
	left, right migrationCorrelationReference,
) (_ int, resultErr error) {
	leftReader, leftHeader, err := s.openRecord(left)
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, leftReader.Close()) }()
	rightReader, rightHeader, err := s.openRecord(right)
	if err != nil {
		return 0, errors.Join(err, leftReader.Close())
	}
	defer func() { resultErr = errors.Join(resultErr, rightReader.Close()) }()
	comparison, err := compareMigrationCorrelationIDs(
		leftReader.buffer, rightReader.buffer, leftHeader.idLength, rightHeader.idLength,
	)
	if err != nil || comparison != 0 {
		return comparison, err
	}
	if leftHeader.sequence != rightHeader.sequence {
		return compareMigrationCorrelationInt64(leftHeader.sequence, rightHeader.sequence), nil
	}
	if leftHeader.ordinal != rightHeader.ordinal {
		return compareMigrationCorrelationInt64(leftHeader.ordinal, rightHeader.ordinal), nil
	}
	return compareMigrationCorrelationInt64(int64(leftHeader.kind), int64(rightHeader.kind)), nil
}

func migrationCorrelationCompareByResolution(
	s *migrationCorrelationSorter,
	left, right migrationCorrelationReference,
) (_ int, resultErr error) {
	leftReader, leftHeader, err := s.openRecord(left)
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, leftReader.Close()) }()
	rightReader, rightHeader, err := s.openRecord(right)
	if err != nil {
		return 0, errors.Join(err, leftReader.Close())
	}
	defer func() { resultErr = errors.Join(resultErr, rightReader.Close()) }()
	if leftHeader.sequence != rightHeader.sequence {
		return compareMigrationCorrelationInt64(leftHeader.sequence, rightHeader.sequence), nil
	}
	return compareMigrationCorrelationInt64(leftHeader.ordinal, rightHeader.ordinal), nil
}

func compareMigrationCorrelationIDs(
	left, right *bufio.Reader,
	leftLength, rightLength uint64,
) (int, error) {
	limit := leftLength
	if rightLength < limit {
		limit = rightLength
	}
	for index := uint64(0); index < limit; index++ {
		leftByte, err := left.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("read migration correlation left call ID: %w", err)
		}
		rightByte, err := right.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("read migration correlation right call ID: %w", err)
		}
		if leftByte < rightByte {
			return -1, nil
		}
		if leftByte > rightByte {
			return 1, nil
		}
	}
	return compareMigrationCorrelationUint64(leftLength, rightLength), nil
}

func compareMigrationCorrelationInt64(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareMigrationCorrelationUint64(left, right uint64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func discardMigrationCorrelationBytes(reader *bufio.Reader, size uint64) error {
	for index := uint64(0); index < size; index++ {
		if _, err := reader.ReadByte(); err != nil {
			return fmt.Errorf("discard migration correlation value: %w", err)
		}
	}
	return nil
}

func (s *migrationCorrelationSorter) sortSources(
	sources []*migrationCorrelationSource,
	compare migrationCorrelationCompare,
) (_ *migrationCorrelationArtifact, resultErr error) {
	inventory, err := s.newOwnedArtifactWriter()
	if err != nil {
		return nil, err
	}
	s.activeInventory = inventory.artifact
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, inventory.Close())
		}
	}()
	var batch []migrationCorrelationReference
	var releases []func()
	var runBytes int64
	releaseBatch := func() {
		for _, release := range releases {
			release()
		}
		batch = nil
		releases = nil
		runBytes = 0
	}
	defer releaseBatch()
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		defer releaseBatch()
		var compareErr error
		sort.SliceStable(batch, func(left, right int) bool {
			if compareErr != nil {
				return false
			}
			comparison, err := compare(s, batch[left], batch[right])
			if err != nil {
				compareErr = err
				return false
			}
			return comparison < 0
		})
		if compareErr != nil {
			return compareErr
		}
		run, err := s.newOwnedArtifactWriter()
		if err != nil {
			return err
		}
		for _, ref := range batch {
			if err := writeMigrationCorrelationReference(run, ref); err != nil {
				return errors.Join(err, run.Close(), s.removeArtifact(run.artifact))
			}
		}
		if err := run.Close(); err != nil {
			return errors.Join(err, s.removeArtifact(run.artifact))
		}
		return writeMigrationRunEntry(inventory, run.artifact)
	}
	for _, source := range sources {
		if source == nil || source.index == nil {
			return nil, fmt.Errorf("migration correlation sort source is unavailable")
		}
		reader, err := s.openReferenceReader(source.index.artifact)
		if err != nil {
			return nil, err
		}
		for {
			if err := s.ctx.Err(); err != nil {
				closeErr := reader.Close()
				return nil, errors.Join(err, closeErr)
			}
			ref, found, err := reader.Next()
			if err != nil {
				closeErr := reader.Close()
				return nil, errors.Join(err, closeErr)
			}
			if !found {
				if err := reader.Close(); err != nil {
					return nil, err
				}
				break
			}
			if runBytes > 0 && runBytes+ref.size > migrationCorrelationRunBudgetBytes {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			release, acquired, err := s.ledger.tryAcquireInline(
				int64(48),
				migrationCorrelationRunBudgetBytes,
			)
			if err != nil {
				return nil, err
			}
			if !acquired {
				if err := flush(); err != nil {
					return nil, err
				}
				release, acquired, err = s.ledger.tryAcquireInline(48, migrationCorrelationRunBudgetBytes)
				if err != nil || !acquired {
					return nil, errors.Join(err, fmt.Errorf("migration correlation run descriptor exceeds memory budget"))
				}
			}
			batch = append(batch, ref)
			releases = append(releases, release)
			runBytes += ref.size
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if err := inventory.Close(); err != nil {
		return nil, err
	}
	return s.mergeRunInventory(inventory.artifact, compare)
}

func writeMigrationRunEntry(writer io.Writer, artifact *migrationCorrelationArtifact) error {
	if artifact == nil {
		return fmt.Errorf("migration correlation run artifact is required")
	}
	if err := writeMigrationCorrelationUvarint(writer, uint64(len(artifact.path))); err != nil {
		return err
	}
	if err := writeMigrationString(writer, artifact.path); err != nil {
		return err
	}
	return writeMigrationCorrelationUvarint(writer, uint64(artifact.size))
}

type migrationCorrelationRunReader struct {
	reader *migrationCorrelationArtifactReader
}

func (s *migrationCorrelationSorter) openRunReader(
	artifact *migrationCorrelationArtifact,
) (*migrationCorrelationRunReader, error) {
	reader, err := s.openArtifactReader(artifact)
	if err != nil {
		return nil, err
	}
	return &migrationCorrelationRunReader{reader: reader}, nil
}

func (r *migrationCorrelationRunReader) Next() (*migrationCorrelationArtifact, bool, error) {
	length, err := binary.ReadUvarint(r.reader.buffer)
	if errors.Is(err, io.EOF) {
		return nil, false, nil
	}
	if err != nil || length == 0 || length > uint64(^uint(0)>>1) {
		return nil, false, fmt.Errorf("read migration correlation run path length: %w", err)
	}
	path := make([]byte, int(length))
	if _, err := io.ReadFull(r.reader.buffer, path); err != nil {
		return nil, false, fmt.Errorf("read migration correlation run path: %w", err)
	}
	size, err := binary.ReadUvarint(r.reader.buffer)
	if err != nil || size > uint64(^uint64(0)>>1) {
		return nil, false, fmt.Errorf("read migration correlation run size: %w", err)
	}
	return &migrationCorrelationArtifact{path: string(path), size: int64(size)}, true, nil
}

func (r *migrationCorrelationRunReader) Close() error {
	if r == nil {
		return nil
	}
	return r.reader.Close()
}

func (s *migrationCorrelationSorter) mergeRunInventory(
	inventory *migrationCorrelationArtifact,
	compare migrationCorrelationCompare,
) (_ *migrationCorrelationArtifact, resultErr error) {
	for {
		reader, err := s.openRunReader(inventory)
		if err != nil {
			return nil, err
		}
		next, err := s.newOwnedArtifactWriter()
		if err != nil {
			return nil, errors.Join(err, reader.Close())
		}
		s.activeInventory, s.nextInventory = inventory, next.artifact
		runCount := 0
		for {
			var group [migrationCorrelationMergeFanIn]*migrationCorrelationArtifact
			count := 0
			for count < len(group) {
				run, found, err := reader.Next()
				if err != nil {
					return nil, errors.Join(err, reader.Close(), next.Close())
				}
				if !found {
					break
				}
				group[count] = run
				count++
			}
			if count == 0 {
				break
			}
			if count == 1 {
				if err := writeMigrationRunEntry(next, group[0]); err != nil {
					return nil, errors.Join(err, reader.Close(), next.Close())
				}
				runCount++
				continue
			}
			merged, err := s.mergeRuns(group[:count], compare)
			if err != nil {
				return nil, errors.Join(err, reader.Close(), next.Close())
			}
			if err := writeMigrationRunEntry(next, merged); err != nil {
				return nil, errors.Join(err, reader.Close(), next.Close())
			}
			runCount++
		}
		if err := errors.Join(reader.Close(), next.Close()); err != nil {
			return nil, err
		}
		if err := s.removeArtifact(inventory); err != nil {
			return nil, err
		}
		s.activeInventory = nil
		if runCount == 0 {
			if err := s.removeArtifact(next.artifact); err != nil {
				return nil, err
			}
			empty, err := s.newOwnedArtifactWriter()
			if err != nil {
				return nil, err
			}
			if err := empty.Close(); err != nil {
				return nil, errors.Join(err, s.removeArtifact(empty.artifact))
			}
			s.nextInventory = nil
			return empty.artifact, nil
		}
		if runCount == 1 {
			finalReader, err := s.openRunReader(next.artifact)
			if err != nil {
				return nil, err
			}
			final, found, readErr := finalReader.Next()
			closeErr := finalReader.Close()
			if readErr != nil || !found {
				return nil, errors.Join(readErr, closeErr, fmt.Errorf("migration correlation final run is missing"))
			}
			if err := s.removeArtifact(next.artifact); err != nil {
				return nil, errors.Join(closeErr, err)
			}
			s.nextInventory = nil
			return final, closeErr
		}
		inventory = next.artifact
		s.activeInventory, s.nextInventory = inventory, nil
	}
}

func (s *migrationCorrelationSorter) mergeRuns(
	inputs []*migrationCorrelationArtifact,
	compare migrationCorrelationCompare,
) (_ *migrationCorrelationArtifact, resultErr error) {
	if len(inputs) < 2 || len(inputs) > migrationCorrelationMergeFanIn {
		return nil, fmt.Errorf("migration correlation merge input count is invalid: %d", len(inputs))
	}
	readers := make([]*migrationCorrelationReferenceReader, len(inputs))
	current := make([]migrationCorrelationReference, len(inputs))
	active := make([]bool, len(inputs))
	for index, input := range inputs {
		reader, err := s.openReferenceReader(input)
		if err != nil {
			return nil, errors.Join(err, closeMigrationCorrelationReferenceReaders(readers))
		}
		readers[index] = reader
		ref, found, err := reader.Next()
		if err != nil {
			return nil, errors.Join(err, closeMigrationCorrelationReferenceReaders(readers))
		}
		current[index], active[index] = ref, found
	}
	output, err := s.newOwnedArtifactWriter()
	if err != nil {
		return nil, errors.Join(err, closeMigrationCorrelationReferenceReaders(readers))
	}
	defer func() {
		resultErr = errors.Join(resultErr, output.Close(), closeMigrationCorrelationReferenceReaders(readers))
	}()
	for {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		selected := -1
		for index, isActive := range active {
			if !isActive {
				continue
			}
			if selected < 0 {
				selected = index
				continue
			}
			comparison, err := compare(s, current[index], current[selected])
			if err != nil {
				return nil, err
			}
			if comparison < 0 {
				selected = index
			}
		}
		if selected < 0 {
			break
		}
		if err := writeMigrationCorrelationReference(output, current[selected]); err != nil {
			return nil, err
		}
		ref, found, err := readers[selected].Next()
		if err != nil {
			return nil, err
		}
		current[selected], active[selected] = ref, found
	}
	for _, input := range inputs {
		if err := s.removeArtifact(input); err != nil {
			return nil, err
		}
	}
	return output.artifact, nil
}

func closeMigrationCorrelationReferenceReaders(readers []*migrationCorrelationReferenceReader) error {
	var errs []error
	for _, reader := range readers {
		if reader != nil {
			errs = append(errs, reader.Close())
		}
	}
	return errors.Join(errs...)
}

func (s *migrationCorrelationSorter) cleanupInventory(slot **migrationCorrelationArtifact) error {
	if slot == nil || *slot == nil {
		return nil
	}
	inventory := *slot
	reader, err := s.openRunReader(inventory)
	if err != nil {
		return err
	}
	var errs []error
	for {
		run, found, nextErr := reader.Next()
		if nextErr != nil {
			errs = append(errs, nextErr)
			break
		}
		if !found {
			break
		}
		errs = append(errs, s.removeArtifact(run))
	}
	errs = append(errs, reader.Close(), s.removeArtifact(inventory))
	if errors.Join(errs...) == nil {
		*slot = nil
	}
	return errors.Join(errs...)
}

func (s *migrationCorrelationSorter) resolveFirstRun(firstRun *migrationCorrelationArtifact) (resultErr error) {
	reader, err := s.openReferenceReader(firstRun)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, reader.Close(), s.removeArtifact(firstRun))
	}()
	var previousCall *migrationCorrelationReference
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		ref, found, err := reader.Next()
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		recordReader, record, err := s.openRecord(ref)
		if err != nil {
			return err
		}
		closeRecord := func() error { return recordReader.Close() }
		switch record.kind {
		case migrationCorrelationCallRecord:
			if err := closeRecord(); err != nil {
				return err
			}
			copyRef := ref
			previousCall = &copyRef
		case migrationCorrelationQueryRecord:
			if err := closeRecord(); err != nil {
				return err
			}
			custom := false
			nameRef := ref
			nameLength := record.nameLength
			if previousCall != nil {
				callReader, call, err := s.openRecord(*previousCall)
				if err != nil {
					return err
				}
				queryReader, query, err := s.openRecord(ref)
				if err != nil {
					return errors.Join(err, callReader.Close())
				}
				matches, compareErr := compareMigrationCorrelationIDs(
					callReader.buffer, queryReader.buffer, call.idLength, query.idLength,
				)
				closeErr := errors.Join(callReader.Close(), queryReader.Close())
				if compareErr != nil || closeErr != nil {
					return errors.Join(compareErr, closeErr)
				}
				if matches == 0 {
					custom = call.custom
					if record.nameLength == 0 {
						nameRef = *previousCall
						nameLength = call.nameLength
					}
				}
			}
			if err := s.appendResolution(ref, record, custom, nameRef, nameLength); err != nil {
				return err
			}
		default:
			return fmt.Errorf("migration correlation first sort contains invalid record kind")
		}
	}
}

func (s *migrationCorrelationSorter) appendResolution(
	queryRef migrationCorrelationReference,
	query migrationCorrelationRecordHeader,
	custom bool,
	nameRef migrationCorrelationReference,
	nameLength uint64,
) error {
	source := &s.resolutions
	offset := source.data.artifact.size
	nameReader, nameHeader, err := s.openRecord(nameRef)
	if err != nil {
		return err
	}
	if err := discardMigrationCorrelationBytes(nameReader.buffer, nameHeader.idLength); err != nil {
		return errors.Join(err, nameReader.Close())
	}
	writeErr := writeMigrationCorrelationRecordPrefix(
		source.data,
		migrationCorrelationResolutionRecord,
		0,
		query.sequence,
		query.ordinal,
		custom,
		nameLength,
		func() error {
			return copyMigrationReaderWithBuffer(source.data, nameReader.buffer, int64(nameLength), s.ledger)
		},
	)
	closeErr := nameReader.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return writeMigrationCorrelationReference(source.index, migrationCorrelationReference{
		source: source.tag, offset: offset, size: source.data.artifact.size - offset,
	})
}

func (s *migrationCorrelationSorter) readResolution(
	ref migrationCorrelationReference,
) (_ migrationCorrelationResolution, resultErr error) {
	reader, header, err := s.openRecord(ref)
	if err != nil {
		return migrationCorrelationResolution{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	if header.kind != migrationCorrelationResolutionRecord || header.idLength != 0 {
		return migrationCorrelationResolution{}, fmt.Errorf("migration correlation stream contains non-resolution record")
	}
	if header.nameLength > uint64(^uint(0)>>1) {
		return migrationCorrelationResolution{}, fmt.Errorf("migration correlation resolved name is too large")
	}
	name := make([]byte, int(header.nameLength))
	if _, err := io.ReadFull(reader.buffer, name); err != nil {
		return migrationCorrelationResolution{}, fmt.Errorf("read migration correlation resolved name: %w", err)
	}
	return migrationCorrelationResolution{
		Sequence: header.sequence, Ordinal: header.ordinal, Custom: header.custom, Name: string(name),
	}, nil
}
