package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"core/server/llm/openaiwire"
	"core/shared/rollbacktarget"
)

type legacyMigrationTransformResult struct {
	DirectSnapshots      int64
	GeneratedSnapshotRaw int64
	AbsentSnapshots      int64
	CorrelationArtifacts int64
}

type legacyFallbackEpoch struct {
	sorter      *migrationCorrelationSorter
	descriptors *migrationCorrelationArtifactWriter
}

type legacyFallbackEpochSeed struct {
	recordRange      migrationJSONValueRange
	normalizerBefore legacySequenceNormalizer
}

type legacySequenceNormalizer struct {
	initialized        bool
	previousNormalized int64
	cumulativeOffset   int64
}

type legacyMigrationOutput struct {
	destination             io.Writer
	bytesWritten            int64
	latestRollbackCandidate *rollbacktarget.CandidateLocator
}

const (
	legacyMigrationDescriptorRecord byte = iota + 1
	legacyMigrationDescriptorFallback
)

func transformLegacyEventLogV0(
	ctx context.Context,
	source io.ReaderAt,
	sourceSize int64,
	destination io.Writer,
	spoolDir string,
	ledger *migrationResourceLedger,
	storage migrationSpoolStorage,
) (result legacyMigrationTransformResult, resultErr error) {
	if ctx == nil {
		return legacyMigrationTransformResult{}, fmt.Errorf("legacy migration context is required")
	}
	if source == nil {
		return legacyMigrationTransformResult{}, fmt.Errorf("legacy migration source is required")
	}
	if sourceSize < 0 {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"legacy migration source size must not be negative: %d",
			sourceSize,
		)
	}
	if destination == nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"legacy migration destination is required",
		)
	}
	if ledger == nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"legacy migration resource ledger is required",
		)
	}
	if storage == nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"legacy migration artifact storage is required",
		)
	}
	if spoolDir == "" {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"legacy migration artifact directory is required",
		)
	}
	header, err := encodeEventLogHeaderV1()
	if err != nil {
		return legacyMigrationTransformResult{}, err
	}
	output := &legacyMigrationOutput{destination: destination}
	if err := writeMigrationBytes(output, append(header, '\n')); err != nil {
		return legacyMigrationTransformResult{}, fmt.Errorf(
			"write migrated event-log header: %w",
			err,
		)
	}

	epochStart := int64(0)
	var epochSeed *legacyFallbackEpochSeed
	var fallbackEpoch *legacyFallbackEpoch
	var sequenceNormalizer legacySequenceNormalizer
	defer func() {
		if fallbackEpoch != nil {
			resultErr = errors.Join(resultErr, fallbackEpoch.sorter.Close())
		}
	}()
	for offset := int64(0); offset < sourceSize; {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		lineRange, nextOffset, terminated, err := nextLegacyEventLineRange(
			source,
			offset,
			sourceSize,
			ledger,
		)
		if err != nil {
			return result, err
		}
		if lineRange.Size() == 0 {
			return result, eventLogContractError{Err: fmt.Errorf(
				"legacy event log contains an empty record at byte %d",
				offset,
			)}
		}
		decoded, decodeErr := decodeLegacyEventV0(
			source,
			lineRange.Start,
			lineRange.End,
			ledger,
		)
		if decodeErr != nil {
			if !terminated && errors.Is(decodeErr, errMigrationJSONMalformed) {
				if fallbackEpoch != nil {
					created, flushErr := flushLegacyFallbackEpoch(
						output,
						fallbackEpoch,
						ledger,
					)
					result.CorrelationArtifacts += int64(created)
					fallbackEpoch = nil
					if flushErr != nil {
						return result, flushErr
					}
				}
				return result, nil
			}
			return result, eventLogContractError{Err: fmt.Errorf(
				"decode legacy event at byte %d: %w",
				offset,
				decodeErr,
			)}
		}
		normalizerBeforeRecord := sequenceNormalizer
		normalizedSequence, err := sequenceNormalizer.Normalize(decoded.Sequence)
		if err != nil {
			return result, eventLogContractError{Err: fmt.Errorf(
				"normalize legacy event sequence at byte %d: %w",
				offset,
				err,
			)}
		}
		decoded, err = withNormalizedLegacySequence(decoded, normalizedSequence)
		if err != nil {
			return result, eventLogContractError{Err: fmt.Errorf(
				"rewrite legacy event sequence at byte %d: %w",
				offset,
				err,
			)}
		}
		switch decoded.SnapshotClass {
		case legacyToolSnapshotAuthoritative:
			result.DirectSnapshots++
		case legacyToolSnapshotGeneratedRaw:
			result.GeneratedSnapshotRaw++
		case legacyToolSnapshotAbsent:
			result.AbsentSnapshots++
		}
		switch {
		case decoded.Dropped:
		case decoded.FallbackCompletion != nil:
			if fallbackEpoch == nil {
				fallbackEpoch, err = startLegacyFallbackEpoch(
					ctx,
					source,
					epochSeed,
					epochStart,
					offset,
					spoolDir,
					ledger,
					storage,
				)
				if err != nil {
					return result, err
				}
			}
			fallback := decoded.FallbackCompletion
			if err := fallbackEpoch.sorter.AddQuery(
				migrationCorrelationCompletionQuery{
					NormalizedCallID: []byte(fallback.CallID),
					Sequence:         fallback.Sequence,
					Ordinal:          0,
					Name:             fallback.Name,
				},
			); err != nil {
				return result, errors.Join(err, fallback.Close(), fallbackEpoch.sorter.Close())
			}
			if err := writeLegacyFallbackDescriptor(
				fallbackEpoch.descriptors,
				fallback,
				0,
			); err != nil {
				return result, errors.Join(err, fallbackEpoch.sorter.Close())
			}
		case decoded.Record != nil:
			kind, err := decoded.Record.Kind()
			if err != nil {
				return result, err
			}
			if kind == EventKindHistoryReplace {
				if fallbackEpoch != nil {
					created, flushErr := flushLegacyFallbackEpoch(
						output,
						fallbackEpoch,
						ledger,
					)
					result.CorrelationArtifacts += int64(created)
					if flushErr != nil {
						return result, flushErr
					}
					fallbackEpoch = nil
				}
				if err := writeMigratedRecord(output, *decoded.Record); err != nil {
					return result, err
				}
				seed := legacyFallbackEpochSeed{
					recordRange:      lineRange,
					normalizerBefore: normalizerBeforeRecord,
				}
				epochSeed = &seed
				epochStart = nextOffset
				offset = nextOffset
				continue
			}
			if fallbackEpoch == nil {
				if err := writeMigratedRecord(output, *decoded.Record); err != nil {
					return result, err
				}
			} else {
				if err := addLegacyRecordCallDefinitions(
					fallbackEpoch.sorter,
					*decoded.Record,
				); err != nil {
					return result, errors.Join(err, fallbackEpoch.sorter.Close())
				}
				if err := writeLegacyRecordDescriptor(
					fallbackEpoch.descriptors,
					*decoded.Record,
				); err != nil {
					return result, errors.Join(err, fallbackEpoch.sorter.Close())
				}
			}
		default:
			return result, fmt.Errorf(
				"legacy event at byte %d produced no migration outcome",
				offset,
			)
		}
		offset = nextOffset
	}
	if fallbackEpoch != nil {
		created, err := flushLegacyFallbackEpoch(output, fallbackEpoch, ledger)
		result.CorrelationArtifacts += int64(created)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (o *legacyMigrationOutput) Write(payload []byte) (int, error) {
	if o == nil || o.destination == nil {
		return 0, fmt.Errorf("legacy migration output is required")
	}
	if int64(len(payload)) > math.MaxInt64-o.bytesWritten {
		return 0, fmt.Errorf(
			"legacy migration output byte cursor overflow: cursor=%d write=%d",
			o.bytesWritten,
			len(payload),
		)
	}
	n, err := o.destination.Write(payload)
	o.bytesWritten += int64(n)
	return n, err
}

func (o *legacyMigrationOutput) writeRecord(record EventRecord) error {
	payload, err := record.Payload()
	if err != nil {
		return err
	}
	if replacement, ok := payload.(HistoryReplacementRecord); ok {
		replacement = rebaseHistoryReplacementRollbackCandidate(
			replacement,
			o.latestRollbackCandidate,
		)
		rebased, err := NewEventRecord(
			record.Seq(),
			record.StepID(),
			replacement,
		)
		if err != nil {
			return fmt.Errorf(
				"rebuild migrated rollback locator for sequence %d: %w",
				record.Seq(),
				err,
			)
		}
		record = rebased
	}
	line, err := encodeEventRecordV1(record)
	if err != nil {
		return fmt.Errorf(
			"encode migrated event sequence %d: %w",
			record.Seq(),
			err,
		)
	}
	line = append(line, '\n')
	if err := writeMigrationBytes(o, line); err != nil {
		return fmt.Errorf(
			"write migrated event sequence %d: %w",
			record.Seq(),
			err,
		)
	}
	visibleUser, err := isForkVisibleUserMessage(record)
	if err != nil {
		return err
	}
	if visibleUser {
		o.observeVisibleUserMessage(record.Seq())
	}
	return nil
}

func (o *legacyMigrationOutput) observeVisibleUserMessage(sequence int64) {
	o.latestRollbackCandidate = &rollbacktarget.CandidateLocator{
		UserMessageSeq:       sequence,
		CandidatePageEndByte: o.bytesWritten,
	}
}

func (n *legacySequenceNormalizer) Normalize(sequence int64) (int64, error) {
	if sequence <= 0 {
		return 0, fmt.Errorf("legacy event sequence must be positive: %d", sequence)
	}
	if sequence > math.MaxInt64-n.cumulativeOffset {
		return 0, fmt.Errorf(
			"legacy event sequence overflows cumulative offset: sequence=%d offset=%d",
			sequence,
			n.cumulativeOffset,
		)
	}
	candidate := sequence + n.cumulativeOffset
	if n.initialized && candidate <= n.previousNormalized {
		if n.previousNormalized == math.MaxInt64 {
			return 0, fmt.Errorf(
				"legacy event sequence cannot advance beyond %d",
				n.previousNormalized,
			)
		}
		candidate = n.previousNormalized + 1
		n.cumulativeOffset = candidate - sequence
	}
	n.initialized = true
	n.previousNormalized = candidate
	return candidate, nil
}

func withNormalizedLegacySequence(
	decoded legacyEventV0DecodeResult,
	sequence int64,
) (legacyEventV0DecodeResult, error) {
	decoded.Sequence = sequence
	if decoded.FallbackCompletion != nil {
		decoded.FallbackCompletion.Sequence = sequence
	}
	if decoded.Record == nil {
		return decoded, nil
	}
	payload, err := decoded.Record.Payload()
	if err != nil {
		return legacyEventV0DecodeResult{}, err
	}
	record, err := NewEventRecord(
		sequence,
		decoded.Record.StepID(),
		payload,
	)
	if err != nil {
		return legacyEventV0DecodeResult{}, err
	}
	decoded.Record = &record
	return decoded, nil
}

func startLegacyFallbackEpoch(
	ctx context.Context,
	source io.ReaderAt,
	seed *legacyFallbackEpochSeed,
	epochStart int64,
	fallbackOffset int64,
	spoolDir string,
	ledger *migrationResourceLedger,
	storage migrationSpoolStorage,
) (*legacyFallbackEpoch, error) {
	sorter, err := newMigrationCorrelationSorter(
		ctx,
		spoolDir,
		ledger,
		storage,
	)
	if err != nil {
		return nil, err
	}
	descriptors, err := sorter.newArtifactWriter()
	if err != nil {
		return nil, errors.Join(err, sorter.Close())
	}
	epoch := &legacyFallbackEpoch{sorter: sorter, descriptors: descriptors}
	if err := rescanLegacyEpochCallDefinitions(
		ctx,
		source,
		seed,
		epochStart,
		fallbackOffset,
		ledger,
		sorter,
	); err != nil {
		return nil, errors.Join(err, sorter.Close())
	}
	return epoch, nil
}

func rescanLegacyEpochCallDefinitions(
	ctx context.Context,
	source io.ReaderAt,
	seed *legacyFallbackEpochSeed,
	start int64,
	end int64,
	ledger *migrationResourceLedger,
	sorter *migrationCorrelationSorter,
) error {
	var sequenceNormalizer legacySequenceNormalizer
	if seed != nil {
		sequenceNormalizer = seed.normalizerBefore
		decoded, err := decodeLegacyEventV0(
			source,
			seed.recordRange.Start,
			seed.recordRange.End,
			ledger,
		)
		if err != nil {
			return fmt.Errorf("decode legacy correlation seed: %w", err)
		}
		if decoded.Record == nil {
			return fmt.Errorf("legacy correlation seed is not semantic history")
		}
		kind, err := decoded.Record.Kind()
		if err != nil {
			return err
		}
		if kind != EventKindHistoryReplace {
			return fmt.Errorf("legacy correlation seed is not semantic history")
		}
		normalizedSequence, err := sequenceNormalizer.Normalize(decoded.Sequence)
		if err != nil {
			return fmt.Errorf("normalize legacy correlation seed sequence: %w", err)
		}
		decoded, err = withNormalizedLegacySequence(decoded, normalizedSequence)
		if err != nil {
			return fmt.Errorf("rewrite legacy correlation seed sequence: %w", err)
		}
		if err := addLegacyRecordCallDefinitions(sorter, *decoded.Record); err != nil {
			return err
		}
	}
	for offset := start; offset < end; {
		if err := ctx.Err(); err != nil {
			return err
		}
		lineRange, nextOffset, terminated, err := nextLegacyEventLineRange(
			source,
			offset,
			end,
			ledger,
		)
		if err != nil {
			return err
		}
		if !terminated && nextOffset != end {
			return fmt.Errorf("legacy correlation prefix line at byte %d is incomplete", offset)
		}
		decoded, err := decodeLegacyEventV0(
			source,
			lineRange.Start,
			lineRange.End,
			ledger,
		)
		if err != nil {
			return fmt.Errorf(
				"decode legacy correlation prefix at byte %d: %w",
				offset,
				err,
			)
		}
		normalizedSequence, err := sequenceNormalizer.Normalize(decoded.Sequence)
		if err != nil {
			return fmt.Errorf(
				"normalize legacy correlation prefix sequence at byte %d: %w",
				offset,
				err,
			)
		}
		decoded, err = withNormalizedLegacySequence(decoded, normalizedSequence)
		if err != nil {
			return fmt.Errorf(
				"rewrite legacy correlation prefix sequence at byte %d: %w",
				offset,
				err,
			)
		}
		if decoded.Record != nil {
			if err := addLegacyRecordCallDefinitions(sorter, *decoded.Record); err != nil {
				return err
			}
		}
		offset = nextOffset
	}
	return nil
}

func addLegacyRecordCallDefinitions(
	sorter *migrationCorrelationSorter,
	record EventRecord,
) error {
	payload, err := record.Payload()
	if err != nil {
		return err
	}
	switch payload := payload.(type) {
	case MessageRecord:
		for index, call := range payload.ToolCalls {
			callID := strings.TrimSpace(call.CallID)
			if callID == "" {
				continue
			}
			if err := sorter.AddCall(migrationCorrelationCallDefinition{
				NormalizedCallID: []byte(callID),
				Sequence:         record.Seq(),
				Ordinal:          int64(index),
				Custom:           call.Kind == ToolCallKindCustom,
				Name:             strings.TrimSpace(call.Name),
			}); err != nil {
				return err
			}
		}
	case HistoryReplacementRecord:
		for index, item := range payload.Items {
			custom := false
			switch item.Type {
			case ProviderHistoryItemTypeFunctionCall:
			case ProviderHistoryItemTypeCustomToolCall:
				custom = true
			default:
				continue
			}
			callID := ""
			if item.CallID != nil {
				callID = strings.TrimSpace(*item.CallID)
			}
			if callID == "" && item.ID != nil {
				callID = strings.TrimSpace(*item.ID)
			}
			if callID == "" {
				continue
			}
			name := ""
			if item.Name != nil {
				name = strings.TrimSpace(*item.Name)
			}
			if err := sorter.AddCall(migrationCorrelationCallDefinition{
				NormalizedCallID: []byte(callID),
				Sequence:         record.Seq(),
				Ordinal:          int64(index),
				Custom:           custom,
				Name:             name,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func flushLegacyFallbackEpoch(
	destination *legacyMigrationOutput,
	epoch *legacyFallbackEpoch,
	ledger *migrationResourceLedger,
) (_ int, resultErr error) {
	if epoch == nil || epoch.sorter == nil || epoch.descriptors == nil {
		return 0, fmt.Errorf("legacy fallback epoch is required")
	}
	defer func() {
		resultErr = errors.Join(resultErr, epoch.sorter.Close())
	}()
	if err := epoch.descriptors.Close(); err != nil {
		return epoch.sorter.CreatedArtifactCount(), err
	}
	resolutions, err := epoch.sorter.Finish()
	if err != nil {
		return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
			"resolve legacy fallback correlation: %w",
			err,
		)
	}
	descriptorReader, err := epoch.sorter.openArtifactReader(
		epoch.descriptors.artifact,
	)
	if err != nil {
		return epoch.sorter.CreatedArtifactCount(), errors.Join(err, resolutions.Close())
	}
	defer func() {
		resultErr = errors.Join(resultErr, descriptorReader.Close(), resolutions.Close())
	}()
	for {
		kind, err := descriptorReader.buffer.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return epoch.sorter.CreatedArtifactCount(), err
		}
		switch kind {
		case legacyMigrationDescriptorRecord:
			visibleUser, err := descriptorReader.buffer.ReadByte()
			if err != nil {
				return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
					"read legacy direct descriptor visible-user fact: %w",
					err,
				)
			}
			var visibleUserSequence *int64
			switch visibleUser {
			case 0:
			case 1:
				sequence, err := binary.ReadUvarint(descriptorReader.buffer)
				if err != nil {
					return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
						"read legacy direct descriptor visible-user sequence: %w",
						err,
					)
				}
				if sequence > math.MaxInt64 {
					return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
						"legacy direct descriptor visible-user sequence is too large: %d",
						sequence,
					)
				}
				sequenceValue := int64(sequence)
				visibleUserSequence = &sequenceValue
			default:
				return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
					"legacy direct descriptor has invalid visible-user fact %d",
					visibleUser,
				)
			}
			size, err := binary.ReadUvarint(descriptorReader.buffer)
			if err != nil {
				return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
					"read legacy direct descriptor size: %w",
					err,
				)
			}
			if err := copyMigrationReaderWithBuffer(
				destination,
				descriptorReader.buffer,
				int64(size),
				ledger,
			); err != nil {
				return epoch.sorter.CreatedArtifactCount(), err
			}
			if err := writeMigrationBytes(destination, []byte{'\n'}); err != nil {
				return epoch.sorter.CreatedArtifactCount(), err
			}
			if visibleUserSequence != nil {
				destination.observeVisibleUserMessage(*visibleUserSequence)
			}
		case legacyMigrationDescriptorFallback:
			if err := flushLegacyFallbackDescriptor(
				destination,
				descriptorReader.buffer,
				&migrationValueStore{
					spoolDir: epoch.sorter.spoolDir,
					ledger:   ledger,
					storage:  epoch.sorter.storage,
				},
				resolutions,
			); err != nil {
				return epoch.sorter.CreatedArtifactCount(), err
			}
		default:
			return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
				"unsupported legacy migration descriptor kind %d",
				kind,
			)
		}
	}
	if leftover, found, err := resolutions.Next(); err != nil {
		return epoch.sorter.CreatedArtifactCount(), err
	} else if found {
		return epoch.sorter.CreatedArtifactCount(), fmt.Errorf(
			"leftover legacy fallback resolution for sequence %d ordinal %d",
			leftover.Sequence,
			leftover.Ordinal,
		)
	}
	if err := epoch.sorter.removeArtifact(epoch.descriptors.artifact); err != nil {
		return epoch.sorter.CreatedArtifactCount(), err
	}
	return epoch.sorter.CreatedArtifactCount(), nil
}

func flushLegacyFallbackDescriptor(
	destination *legacyMigrationOutput,
	reader *bufio.Reader,
	store *migrationValueStore,
	resolutions *migrationCorrelationResolutionStream,
) (resultErr error) {
	fallback, ordinal, err := readLegacyFallbackDescriptor(reader, store)
	if err != nil {
		return fmt.Errorf("read legacy fallback descriptor: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, fallback.Close())
	}()
	resolution, found, err := resolutions.Next()
	if err != nil {
		return fmt.Errorf("read legacy fallback resolution: %w", err)
	}
	if !found {
		return fmt.Errorf(
			"missing legacy fallback resolution for sequence %d ordinal %d",
			fallback.Sequence,
			ordinal,
		)
	}
	if resolution.Sequence != fallback.Sequence ||
		resolution.Ordinal != ordinal {
		return fmt.Errorf(
			"legacy fallback resolution key mismatch: descriptor=(%d,%d) resolution=(%d,%d)",
			fallback.Sequence,
			ordinal,
			resolution.Sequence,
			resolution.Ordinal,
		)
	}
	return writeResolvedLegacyFallbackRecord(destination, fallback, resolution)
}

func writeResolvedLegacyFallbackRecord(
	destination *legacyMigrationOutput,
	fallback *legacyToolCompletionFallback,
	resolution migrationCorrelationResolution,
) error {
	if destination == nil {
		return fmt.Errorf("legacy migration output is required")
	}
	if fallback == nil || fallback.Output == nil {
		return fmt.Errorf("legacy fallback completion is required")
	}
	outputKind := ToolOutputKindFunction
	itemType := ProviderInputItemTypeFunctionCallOutput
	if resolution.Custom {
		outputKind = ToolOutputKindCustom
		itemType = ProviderInputItemTypeCustomToolOutput
	}
	callID := fallback.CallID
	name := resolution.Name
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("legacy fallback tool name is required")
	}
	if err := writeMigrationBytes(destination, []byte(`{"seq":`)); err != nil {
		return err
	}
	if err := writeMigrationMarshaledJSON(destination, fallback.Sequence); err != nil {
		return err
	}
	if err := writeMigrationBytes(destination, []byte(`,"kind":"tool_completed"`)); err != nil {
		return err
	}
	if fallback.StepID != nil {
		if err := writeMigrationBytes(destination, []byte(`,"step_id":`)); err != nil {
			return err
		}
		if err := writeMigrationMarshaledJSON(destination, fallback.StepID); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		prefix string
		value  any
	}{
		{`,"payload":{"call_id":`, callID},
		{`,"name":`, name},
		{`,"output_kind":`, outputKind},
		{`,"is_error":`, fallback.IsError},
	} {
		if err := writeMigrationBytes(destination, []byte(field.prefix)); err != nil {
			return err
		}
		if err := writeMigrationMarshaledJSON(destination, field.value); err != nil {
			return err
		}
	}
	if err := writeMigrationBytes(destination, []byte(`,"output":`)); err != nil {
		return err
	}
	if err := fallback.Output.CopyTo(destination); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"summary", fallback.Summary},
		{"condensed_text", fallback.CondensedText},
	} {
		if field.value == nil {
			continue
		}
		if err := writeMigrationBytes(destination, []byte(`,"`+field.name+`":`)); err != nil {
			return err
		}
		if err := writeMigrationMarshaledJSON(destination, field.value); err != nil {
			return err
		}
	}
	if fallback.Presentation != nil {
		if err := writeMigrationBytes(destination, []byte(`,"presentation":`)); err != nil {
			return err
		}
		if err := fallback.Presentation.CopyTo(destination); err != nil {
			return err
		}
	}
	if err := writeMigrationBytes(destination, []byte(`,"provider_items":[{"type":`)); err != nil {
		return err
	}
	if err := writeMigrationMarshaledJSON(destination, itemType); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"name", name},
		{"call_id", callID},
	} {
		if err := writeMigrationBytes(destination, []byte(`,"`+field.name+`":`)); err != nil {
			return err
		}
		if err := writeMigrationMarshaledJSON(destination, field.value); err != nil {
			return err
		}
	}
	if err := writeMigrationBytes(destination, []byte(`,"raw":`)); err != nil {
		return err
	}
	if err := writeLegacyFallbackProviderRaw(
		destination,
		itemType,
		callID,
		fallback.Output,
	); err != nil {
		return err
	}
	return writeMigrationBytes(destination, []byte(`}]}}`+"\n"))
}

func writeMigrationMarshaledJSON(writer io.Writer, value any) error {
	encoded, err := marshalSessionJSON(value)
	if err != nil {
		return err
	}
	return writeMigrationBytes(writer, encoded)
}

func writeMigratedRecord(destination *legacyMigrationOutput, record EventRecord) error {
	return destination.writeRecord(record)
}

func writeLegacyRecordDescriptor(
	writer io.Writer,
	record EventRecord,
) error {
	line, err := encodeEventRecordV1(record)
	if err != nil {
		return err
	}
	if err := writeMigrationBytes(
		writer,
		[]byte{legacyMigrationDescriptorRecord},
	); err != nil {
		return err
	}
	visibleUser := byte(0)
	isVisibleUser, err := isForkVisibleUserMessage(record)
	if err != nil {
		return err
	}
	if isVisibleUser {
		visibleUser = 1
	}
	if err := writeMigrationBytes(writer, []byte{visibleUser}); err != nil {
		return err
	}
	if visibleUser == 1 {
		if err := writeMigrationCorrelationUvarint(writer, uint64(record.Seq())); err != nil {
			return err
		}
	}
	if err := writeMigrationCorrelationUvarint(writer, uint64(len(line))); err != nil {
		return err
	}
	return writeMigrationBytes(writer, line)
}

func writeLegacyFallbackDescriptor(
	writer io.Writer,
	fallback *legacyToolCompletionFallback,
	ordinal int64,
) (resultErr error) {
	if fallback == nil || fallback.Output == nil {
		return fmt.Errorf("legacy fallback completion is required")
	}
	defer func() {
		resultErr = errors.Join(resultErr, fallback.Close())
	}()
	if err := writeMigrationBytes(
		writer,
		[]byte{legacyMigrationDescriptorFallback},
	); err != nil {
		return err
	}
	for _, value := range []uint64{uint64(fallback.Sequence), uint64(ordinal)} {
		if err := writeMigrationCorrelationUvarint(writer, value); err != nil {
			return err
		}
	}
	if err := writeLegacyOptionalString(writer, fallback.StepID); err != nil {
		return err
	}
	if err := writeLegacyDescriptorString(writer, fallback.CallID); err != nil {
		return err
	}
	if err := writeLegacyDescriptorString(writer, fallback.Name); err != nil {
		return err
	}
	errorByte := byte(0)
	if fallback.IsError {
		errorByte = 1
	}
	if err := writeMigrationBytes(writer, []byte{errorByte}); err != nil {
		return err
	}
	if err := writeLegacyDescriptorValueSource(writer, fallback.Output); err != nil {
		return err
	}
	if err := writeLegacyOptionalString(writer, fallback.Summary); err != nil {
		return err
	}
	if err := writeLegacyOptionalString(writer, fallback.CondensedText); err != nil {
		return err
	}
	return writeLegacyDescriptorValueSource(writer, fallback.Presentation)
}

func readLegacyFallbackDescriptor(
	reader *bufio.Reader,
	store *migrationValueStore,
) (_ *legacyToolCompletionFallback, _ int64, resultErr error) {
	if store == nil {
		return nil, 0, fmt.Errorf("legacy fallback descriptor value store is required")
	}
	sequence, err := binary.ReadUvarint(reader)
	if err != nil || sequence > uint64(^uint64(0)>>1) {
		return nil, 0, fmt.Errorf(
			"read legacy fallback descriptor sequence: %w",
			err,
		)
	}
	ordinal, err := binary.ReadUvarint(reader)
	if err != nil || ordinal > uint64(^uint64(0)>>1) {
		return nil, 0, fmt.Errorf(
			"read legacy fallback descriptor ordinal: %w",
			err,
		)
	}
	stepID, err := readLegacyOptionalString(reader)
	if err != nil {
		return nil, 0, err
	}
	callID, err := readLegacyDescriptorString(reader)
	if err != nil {
		return nil, 0, err
	}
	name, err := readLegacyDescriptorString(reader)
	if err != nil {
		return nil, 0, err
	}
	errorByte, err := reader.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	if errorByte > 1 {
		return nil, 0, fmt.Errorf(
			"legacy fallback descriptor has invalid error fact %d",
			errorByte,
		)
	}
	output, err := readLegacyDescriptorValueSource(reader, store)
	if err != nil {
		return nil, 0, err
	}
	fallback := &legacyToolCompletionFallback{Output: output}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, fallback.Close())
		}
	}()
	summary, err := readLegacyOptionalString(reader)
	if err != nil {
		return nil, 0, err
	}
	condensedText, err := readLegacyOptionalString(reader)
	if err != nil {
		return nil, 0, err
	}
	presentation, err := readLegacyDescriptorValueSource(reader, store)
	if err != nil {
		return nil, 0, err
	}
	*fallback = legacyToolCompletionFallback{
		Sequence:      int64(sequence),
		StepID:        stepID,
		CallID:        callID,
		Name:          name,
		IsError:       errorByte == 1,
		Output:        output,
		Summary:       summary,
		CondensedText: condensedText,
		Presentation:  presentation,
	}
	return fallback, int64(ordinal), nil
}

func writeLegacyOptionalString(writer io.Writer, value *string) error {
	if value == nil {
		return writeMigrationBytes(writer, []byte{0})
	}
	if err := writeMigrationBytes(writer, []byte{1}); err != nil {
		return err
	}
	return writeLegacyDescriptorString(writer, *value)
}

func readLegacyOptionalString(reader *bufio.Reader) (*string, error) {
	present, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch present {
	case 0:
		return nil, nil
	case 1:
		value, err := readLegacyDescriptorString(reader)
		if err != nil {
			return nil, err
		}
		return &value, nil
	default:
		return nil, fmt.Errorf(
			"legacy fallback descriptor has invalid optional-string fact %d",
			present,
		)
	}
}

func writeLegacyDescriptorBytes(writer io.Writer, value []byte) error {
	if err := writeMigrationCorrelationUvarint(writer, uint64(len(value))); err != nil {
		return err
	}
	return writeMigrationBytes(writer, value)
}

func writeLegacyDescriptorValueSource(
	writer io.Writer,
	value *migrationValueSource,
) error {
	if value == nil {
		return writeMigrationCorrelationUvarint(writer, 0)
	}
	if err := writeMigrationCorrelationUvarint(writer, uint64(value.Size())); err != nil {
		return err
	}
	return value.CopyTo(writer)
}

func readLegacyDescriptorValueSource(
	reader *bufio.Reader,
	store *migrationValueStore,
) (*migrationValueSource, error) {
	size, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	if size > math.MaxInt64 {
		return nil, fmt.Errorf("legacy fallback descriptor value is too large: %d", size)
	}
	return store.RetainReader(reader, int64(size))
}

func writeLegacyFallbackProviderRaw(
	writer io.Writer,
	itemType ProviderInputItemType,
	callID string,
	output *migrationValueSource,
) error {
	if output == nil {
		return fmt.Errorf("legacy fallback provider output is required")
	}
	scratch := migrationProviderEncoderScratch{ledger: output.ledger}
	switch itemType {
	case ProviderInputItemTypeFunctionCallOutput:
		return openaiwire.WriteFunctionCallOutput(writer, callID, output, scratch)
	case ProviderInputItemTypeCustomToolOutput:
		return openaiwire.WriteCustomToolOutput(writer, callID, output, scratch)
	default:
		return fmt.Errorf("unsupported legacy fallback provider item type %q", itemType)
	}
}

type migrationProviderEncoderScratch struct {
	ledger *migrationResourceLedger
}

func (s migrationProviderEncoderScratch) Acquire(
	size int,
) ([]byte, func(), error) {
	release, err := s.ledger.acquireEncoderMerge(int64(size))
	if err != nil {
		return nil, nil, err
	}
	return make([]byte, size), release, nil
}

func writeLegacyDescriptorString(writer io.Writer, value string) error {
	if err := writeMigrationCorrelationUvarint(writer, uint64(len(value))); err != nil {
		return err
	}
	return writeMigrationString(writer, value)
}

func readLegacyDescriptorString(reader *bufio.Reader) (string, error) {
	value, err := readLegacyDescriptorBytes(reader)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func readLegacyDescriptorBytes(reader *bufio.Reader) ([]byte, error) {
	size, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}
	maxInt := uint64(^uint(0) >> 1)
	if size > maxInt {
		return nil, fmt.Errorf(
			"legacy fallback descriptor value is too large: %d",
			size,
		)
	}
	value := make([]byte, int(size))
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, err
	}
	return value, nil
}

func nextLegacyEventLineRange(
	source io.ReaderAt,
	start int64,
	end int64,
	ledger *migrationResourceLedger,
) (migrationJSONValueRange, int64, bool, error) {
	if start < 0 || end < start {
		return migrationJSONValueRange{}, start, false, fmt.Errorf(
			"legacy event line range is invalid: [%d,%d)",
			start,
			end,
		)
	}
	release, err := ledger.acquireSourceDecoder(migrationSourceBufferBytes)
	if err != nil {
		return migrationJSONValueRange{}, start, false, err
	}
	defer release()
	buffer := make([]byte, migrationSourceBufferBytes)
	position := start
	for position < end {
		readLength := int64(len(buffer))
		if remaining := end - position; readLength > remaining {
			readLength = remaining
		}
		n, readErr := source.ReadAt(buffer[:readLength], position)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return migrationJSONValueRange{}, start, false, fmt.Errorf(
				"read legacy event line at byte %d: %w",
				position,
				readErr,
			)
		}
		if newline := bytes.IndexByte(buffer[:n], '\n'); newline >= 0 {
			lineEnd := position + int64(newline)
			return migrationJSONValueRange{Start: start, End: lineEnd},
				lineEnd + 1,
				true,
				nil
		}
		if n == 0 {
			break
		}
		position += int64(n)
	}
	return migrationJSONValueRange{Start: start, End: end}, end, false, nil
}
