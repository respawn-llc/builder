package openaiwire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"core/shared/boundedjson"
	"github.com/go-json-experiment/json/jsontext"
)

type JSONSourceReader interface {
	io.Reader
	io.ReaderAt
	io.Closer
}

// ValidatedJSONSource is a reopenable JSON value whose syntax has already
// been validated by its owning boundary.
type ValidatedJSONSource interface {
	Size() int64
	Open() (JSONSourceReader, error)
}

type ScratchAllocator interface {
	Acquire(size int) (buffer []byte, release func(), err error)
}

func WriteFunctionCallOutput(
	writer io.Writer,
	callID string,
	output ValidatedJSONSource,
	scratch ScratchAllocator,
) error {
	return writeOutput(writer, "function_call_output", callID, output, scratch, true)
}

func WriteCustomToolOutput(
	writer io.Writer,
	callID string,
	output ValidatedJSONSource,
	scratch ScratchAllocator,
) error {
	return writeOutput(writer, "custom_tool_call_output", callID, output, scratch, false)
}

func writeOutput(
	writer io.Writer,
	kind string,
	callID string,
	output ValidatedJSONSource,
	scratch ScratchAllocator,
	structuredContent bool,
) error {
	if writer == nil {
		return fmt.Errorf("OpenAI wire output destination is required")
	}
	normalizedCallID, err := normalizeCallID(callID)
	if err != nil {
		return err
	}
	if output == nil {
		return &ValidationError{Kind: ValidationInvalidOutput}
	}
	if scratch == nil {
		return fmt.Errorf("OpenAI wire output scratch allocator is required")
	}
	if err := writeAll(writer, []byte(`{"type":`)); err != nil {
		return err
	}
	if err := writeJSONValue(writer, kind); err != nil {
		return err
	}
	if err := writeAll(writer, []byte(`,"call_id":`)); err != nil {
		return err
	}
	if err := writeJSONValue(writer, normalizedCallID); err != nil {
		return err
	}
	if err := writeAll(writer, []byte(`,"output":`)); err != nil {
		return err
	}
	if err := writeProviderOutputValue(writer, output, scratch, structuredContent); err != nil {
		return err
	}
	return writeAll(writer, []byte(`}`))
}

func writeProviderOutputValue(
	writer io.Writer,
	output ValidatedJSONSource,
	scratch ScratchAllocator,
	structuredContent bool,
) error {
	reader, err := output.Open()
	if err != nil {
		return err
	}
	var first [1]byte
	_, err = reader.ReadAt(first[:], 0)
	if err != nil {
		return errors.Join(err, reader.Close())
	}
	switch first[0] {
	case '"':
		_, err = io.CopyN(writer, reader, output.Size())
	case '[':
		isStructured := false
		if structuredContent {
			isStructured, err = validatedInputContentSource(output, scratch)
		}
		if err == nil && isStructured {
			err = writeInputContentSource(writer, output, scratch)
		} else if err == nil {
			err = writeJSONStringFromReader(writer, io.NewSectionReader(reader, 0, output.Size()))
		}
	default:
		err = writeJSONStringFromReader(writer, io.NewSectionReader(reader, 0, output.Size()))
	}
	return errors.Join(err, reader.Close())
}

const (
	inputContentScannerBytes   = 64 << 10
	inputContentLibraryMaxSize = inputContentScannerBytes
)

const (
	inputContentTypeSlot = iota
	inputContentTextSlot
	inputContentImageURLSlot
	inputContentDetailSlot
	inputContentFileIDSlot
	inputContentFileDataSlot
	inputContentFileURLSlot
	inputContentFilenameSlot
	inputContentFieldCount
)

var inputContentFields = boundedjson.KnownFields{
	"type",
	"text",
	"image_url",
	"detail",
	"file_id",
	"file_data",
	"file_url",
	"filename",
}

type stringWindow struct {
	start int64
	end   int64
}

func (w stringWindow) nonEmpty() bool {
	return w.end > w.start
}

type canonicalInputContentItem struct {
	kind               string
	detail             string
	values             [inputContentFieldCount]boundedjson.Range
	windows            [inputContentFieldCount]stringWindow
	materializedValues [inputContentFieldCount]string
	present            [inputContentFieldCount]bool
	materialized       bool
}

func validatedInputContentSource(
	source ValidatedJSONSource,
	scratch ScratchAllocator,
) (bool, error) {
	count := 0
	valid := true
	err := scanInputContentSource(source, scratch, func(_ JSONSourceReader, item canonicalInputContentItem) error {
		count++
		if item.kind == "" {
			valid = false
		}
		return nil
	})
	return err == nil && valid && count > 0, err
}

func scanInputContentSource(
	source ValidatedJSONSource,
	scratch ScratchAllocator,
	visit func(JSONSourceReader, canonicalInputContentItem) error,
) error {
	// jsontext owns normal structural parsing, duplicate-name semantics, and
	// string decoding. Its decoder keeps an individual scalar contiguous, so
	// oversized values use the bounded range scanner to preserve the migration
	// hard-memory contract.
	if source.Size() <= inputContentLibraryMaxSize {
		return scanLibraryInputContentSource(source, scratch, visit)
	}
	return scanBoundedInputContentSource(source, scratch, visit)
}

func scanLibraryInputContentSource(
	source ValidatedJSONSource,
	scratch ScratchAllocator,
	visit func(JSONSourceReader, canonicalInputContentItem) error,
) (resultErr error) {
	reader, err := source.Open()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, reader.Close())
	}()
	buffer, release, err := scratch.Acquire(int(source.Size()))
	if err != nil {
		return err
	}
	defer release()
	if int64(len(buffer)) < source.Size() {
		return fmt.Errorf(
			"OpenAI wire output scratch buffer is too small: got=%d want=%d",
			len(buffer),
			source.Size(),
		)
	}
	if _, err := io.ReadFull(reader, buffer[:source.Size()]); err != nil {
		return err
	}
	decoder := jsontext.NewDecoder(
		bytes.NewReader(buffer[:source.Size()]),
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
	)
	token, err := decoder.ReadToken()
	if err != nil || token.Kind() != '[' {
		return &ValidationError{Kind: ValidationInvalidOutput}
	}
	for decoder.PeekKind() != ']' {
		item, err := readLibraryInputContentItem(decoder)
		if err != nil {
			return err
		}
		if err := visit(reader, item); err != nil {
			return err
		}
	}
	token, err = decoder.ReadToken()
	if err != nil || token.Kind() != ']' || decoder.PeekKind() != 0 {
		return &ValidationError{Kind: ValidationInvalidOutput}
	}
	return nil
}

func readLibraryInputContentItem(
	decoder *jsontext.Decoder,
) (canonicalInputContentItem, error) {
	if decoder.PeekKind() != '{' {
		if err := decoder.SkipValue(); err != nil {
			return canonicalInputContentItem{}, err
		}
		return canonicalInputContentItem{}, nil
	}
	if _, err := decoder.ReadToken(); err != nil {
		return canonicalInputContentItem{}, err
	}
	var (
		values            [inputContentFieldCount]string
		present           [inputContentFieldCount]bool
		invalidKnownValue bool
	)
	for decoder.PeekKind() != '}' {
		key, err := decoder.ReadToken()
		if err != nil || key.Kind() != '"' {
			return canonicalInputContentItem{}, &ValidationError{Kind: ValidationInvalidOutput}
		}
		slot := inputContentFieldSlot(key.String())
		if slot < 0 {
			if err := decoder.SkipValue(); err != nil {
				return canonicalInputContentItem{}, err
			}
			continue
		}
		present[slot] = true
		switch decoder.PeekKind() {
		case '"':
			value, err := decoder.ReadToken()
			if err != nil {
				return canonicalInputContentItem{}, err
			}
			values[slot] = value.String()
		case 'n':
			if _, err := decoder.ReadToken(); err != nil {
				return canonicalInputContentItem{}, err
			}
			values[slot] = ""
		default:
			invalidKnownValue = true
			if err := decoder.SkipValue(); err != nil {
				return canonicalInputContentItem{}, err
			}
		}
	}
	if _, err := decoder.ReadToken(); err != nil {
		return canonicalInputContentItem{}, err
	}
	return normalizeMaterializedInputContentObject(values, present, invalidKnownValue), nil
}

func inputContentFieldSlot(name string) int {
	for slot, known := range inputContentFields {
		if strings.EqualFold(name, known) {
			return slot
		}
	}
	return -1
}

func normalizeMaterializedInputContentObject(
	values [inputContentFieldCount]string,
	present [inputContentFieldCount]bool,
	invalidKnownValue bool,
) canonicalInputContentItem {
	item := canonicalInputContentItem{
		materializedValues: values,
		present:            present,
		materialized:       true,
	}
	if invalidKnownValue {
		return item
	}
	for slot := range values {
		if slot != inputContentTextSlot {
			item.materializedValues[slot] = strings.TrimSpace(values[slot])
		}
	}
	switch strings.ToLower(item.materializedValues[inputContentTypeSlot]) {
	case "input_text":
		item.kind = "input_text"
	case "input_image":
		if !item.hasValue(inputContentImageURLSlot) &&
			!item.hasValue(inputContentFileIDSlot) {
			return canonicalInputContentItem{materialized: true}
		}
		item.kind = "input_image"
		switch detail := strings.ToLower(item.materializedValues[inputContentDetailSlot]); detail {
		case "low", "high", "auto":
			item.detail = detail
		}
	case "input_file":
		if !item.hasValue(inputContentFileDataSlot) &&
			!item.hasValue(inputContentFileURLSlot) &&
			!item.hasValue(inputContentFileIDSlot) {
			return canonicalInputContentItem{materialized: true}
		}
		item.kind = "input_file"
	}
	return item
}

func (i canonicalInputContentItem) hasValue(slot int) bool {
	if i.materialized {
		return i.materializedValues[slot] != ""
	}
	return i.windows[slot].nonEmpty()
}

func scanBoundedInputContentSource(
	source ValidatedJSONSource,
	scratch ScratchAllocator,
	visit func(JSONSourceReader, canonicalInputContentItem) error,
) (resultErr error) {
	reader, err := source.Open()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, reader.Close())
	}()
	outerBuffer, releaseOuter, err := scratch.Acquire(inputContentScannerBytes)
	if err != nil {
		return err
	}
	defer releaseOuter()
	if len(outerBuffer) < inputContentScannerBytes {
		return fmt.Errorf(
			"OpenAI wire output scratch buffer is too small: got=%d want=%d",
			len(outerBuffer),
			inputContentScannerBytes,
		)
	}
	scanner, err := boundedjson.NewScanner(
		reader,
		0,
		source.Size(),
		outerBuffer[:inputContentScannerBytes],
		true,
	)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, scanner.Close())
	}()
	return scanner.ScanArray(func(_ int, itemRange boundedjson.Range) error {
		var first [1]byte
		if _, err := reader.ReadAt(first[:], itemRange.Start); err != nil {
			return err
		}
		if first[0] != '{' {
			return visit(reader, canonicalInputContentItem{})
		}
		itemBuffer, releaseItem, err := scratch.Acquire(inputContentScannerBytes)
		if err != nil {
			return err
		}
		defer releaseItem()
		if len(itemBuffer) < inputContentScannerBytes {
			return fmt.Errorf(
				"OpenAI wire output scratch buffer is too small: got=%d want=%d",
				len(itemBuffer),
				inputContentScannerBytes,
			)
		}
		itemScanner, err := boundedjson.NewScanner(
			reader,
			itemRange.Start,
			itemRange.End,
			itemBuffer[:inputContentScannerBytes],
			true,
		)
		if err != nil {
			return err
		}
		invalidKnownValue := false
		object, scanErr := itemScanner.ScanObjectFields(
			inputContentFields,
			func(_ int, value boundedjson.Range) error {
				valid, err := stringOrNullValue(reader, value)
				if err != nil {
					return err
				}
				if !valid {
					invalidKnownValue = true
				}
				return nil
			},
		)
		closeErr := itemScanner.Close()
		if scanErr != nil || closeErr != nil {
			return errors.Join(scanErr, closeErr)
		}
		item, err := normalizeInputContentObject(reader, object, invalidKnownValue)
		if err != nil {
			return err
		}
		return visit(reader, item)
	})
}

func stringOrNullValue(reader io.ReaderAt, value boundedjson.Range) (bool, error) {
	var first [1]byte
	if _, err := reader.ReadAt(first[:], value.Start); err != nil {
		return false, err
	}
	return first[0] == '"' || first[0] == 'n', nil
}

func normalizeInputContentObject(
	reader io.ReaderAt,
	object boundedjson.ScannedObject,
	invalidKnownValue bool,
) (canonicalInputContentItem, error) {
	var item canonicalInputContentItem
	if invalidKnownValue {
		return item, nil
	}
	for slot := 0; slot < inputContentFieldCount; slot++ {
		value, present := object.Value(slot)
		item.values[slot] = value
		item.present[slot] = present
		if !present {
			continue
		}
		trim := slot != inputContentTextSlot
		window, err := analyzeJSONString(reader, value, trim)
		if err != nil {
			return canonicalInputContentItem{}, err
		}
		item.windows[slot] = window
	}
	kind, err := normalizedSmallString(
		reader,
		item.values[inputContentTypeSlot],
		item.windows[inputContentTypeSlot],
	)
	if err != nil {
		return canonicalInputContentItem{}, err
	}
	switch strings.ToLower(kind) {
	case "input_text":
		item.kind = "input_text"
	case "input_image":
		if !item.windows[inputContentImageURLSlot].nonEmpty() &&
			!item.windows[inputContentFileIDSlot].nonEmpty() {
			return canonicalInputContentItem{}, nil
		}
		item.kind = "input_image"
		detail, err := normalizedSmallString(
			reader,
			item.values[inputContentDetailSlot],
			item.windows[inputContentDetailSlot],
		)
		if err != nil {
			return canonicalInputContentItem{}, err
		}
		switch strings.ToLower(detail) {
		case "low", "high", "auto":
			item.detail = strings.ToLower(detail)
		}
	case "input_file":
		if !item.windows[inputContentFileDataSlot].nonEmpty() &&
			!item.windows[inputContentFileURLSlot].nonEmpty() &&
			!item.windows[inputContentFileIDSlot].nonEmpty() {
			return canonicalInputContentItem{}, nil
		}
		item.kind = "input_file"
	}
	return item, nil
}

func normalizedSmallString(
	reader io.ReaderAt,
	value boundedjson.Range,
	window stringWindow,
) (string, error) {
	return materializeJSONStringWindow(reader, value, window, boundedjson.MaxKnownFieldNameBytes)
}

func materializeJSONStringWindow(
	reader io.ReaderAt,
	value boundedjson.Range,
	window stringWindow,
	maxBytes int,
) (string, error) {
	if !window.nonEmpty() {
		return "", nil
	}
	var builder strings.Builder
	if err := visitJSONString(
		reader,
		value,
		func(index int64, decoded rune) error {
			if index >= window.start && index < window.end {
				if maxBytes > 0 && builder.Len() >= maxBytes {
					return nil
				}
				builder.WriteRune(decoded)
			}
			return nil
		},
	); err != nil {
		return "", err
	}
	if maxBytes > 0 &&
		int64(len([]rune(builder.String()))) != window.end-window.start {
		return "", nil
	}
	return builder.String(), nil
}

func analyzeJSONString(
	reader io.ReaderAt,
	value boundedjson.Range,
	trim bool,
) (stringWindow, error) {
	if value.Size() == 0 {
		return stringWindow{}, nil
	}
	var first [1]byte
	if _, err := reader.ReadAt(first[:], value.Start); err != nil {
		return stringWindow{}, err
	}
	if first[0] == 'n' {
		return stringWindow{}, nil
	}
	var (
		count                 int64
		leadingSpaceRunes     int64
		lastNonSpaceExclusive int64
		seenNonSpace          bool
	)
	err := visitJSONString(reader, value, func(_ int64, decoded rune) error {
		count++
		if !trim {
			lastNonSpaceExclusive = count
			return nil
		}
		if !seenNonSpace && unicode.IsSpace(decoded) {
			leadingSpaceRunes++
			return nil
		}
		seenNonSpace = true
		if !unicode.IsSpace(decoded) {
			lastNonSpaceExclusive = count
		}
		return nil
	})
	if err != nil {
		return stringWindow{}, err
	}
	if !trim {
		return stringWindow{end: count}, nil
	}
	return stringWindow{
		start: leadingSpaceRunes,
		end:   lastNonSpaceExclusive,
	}, nil
}

func writeJSONStringRange(
	writer io.Writer,
	reader io.ReaderAt,
	value boundedjson.Range,
	window stringWindow,
) error {
	if err := writeAll(writer, []byte{'"'}); err != nil {
		return err
	}
	if err := visitJSONString(reader, value, func(index int64, decoded rune) error {
		if index < window.start || window.end >= 0 && index >= window.end {
			return nil
		}
		return writeJSONEncodedRune(writer, decoded)
	}); err != nil {
		return err
	}
	return writeAll(writer, []byte{'"'})
}

func visitJSONString(
	reader io.ReaderAt,
	value boundedjson.Range,
	visit func(index int64, decoded rune) error,
) error {
	if value.Size() < 2 {
		return &ValidationError{Kind: ValidationInvalidOutput}
	}
	buffered := bufio.NewReader(io.NewSectionReader(reader, value.Start, value.Size()))
	first, err := buffered.ReadByte()
	if err != nil || first != '"' {
		return &ValidationError{Kind: ValidationInvalidOutput}
	}
	var index int64
	for {
		current, err := buffered.ReadByte()
		if err != nil {
			return err
		}
		switch current {
		case '"':
			if _, err := buffered.Peek(1); !errors.Is(err, io.EOF) {
				if err == nil {
					return &ValidationError{Kind: ValidationInvalidOutput}
				}
				return err
			}
			return nil
		case '\\':
			escaped, err := buffered.ReadByte()
			if err != nil {
				return err
			}
			var decoded rune
			switch escaped {
			case '"', '\\', '/':
				decoded = rune(escaped)
			case 'b':
				decoded = '\b'
			case 'f':
				decoded = '\f'
			case 'n':
				decoded = '\n'
			case 'r':
				decoded = '\r'
			case 't':
				decoded = '\t'
			case 'u':
				decoded, err = readJSONUnicodeEscape(buffered)
				if err != nil {
					return err
				}
				if utf16.IsSurrogate(decoded) {
					prefix, prefixErr := buffered.Peek(2)
					if prefixErr == nil && prefix[0] == '\\' && prefix[1] == 'u' {
						_, _ = buffered.Discard(2)
						trailing, trailingErr := readJSONUnicodeEscape(buffered)
						if trailingErr != nil {
							return trailingErr
						}
						decoded = utf16.DecodeRune(decoded, trailing)
					}
					if decoded == utf8.RuneError || utf16.IsSurrogate(decoded) {
						decoded = utf8.RuneError
					}
				}
			default:
				return &ValidationError{Kind: ValidationInvalidOutput}
			}
			if err := visit(index, decoded); err != nil {
				return err
			}
			index++
		default:
			var decoded rune
			if current < utf8.RuneSelf {
				decoded = rune(current)
			} else {
				if err := buffered.UnreadByte(); err != nil {
					return err
				}
				decoded, _, err = buffered.ReadRune()
				if err != nil {
					return err
				}
			}
			if err := visit(index, decoded); err != nil {
				return err
			}
			index++
		}
	}
}

func writeInputContentSource(
	writer io.Writer,
	source ValidatedJSONSource,
	scratch ScratchAllocator,
) error {
	if err := writeAll(writer, []byte{'['}); err != nil {
		return err
	}
	index := 0
	err := scanInputContentSource(source, scratch, func(
		reader JSONSourceReader,
		item canonicalInputContentItem,
	) error {
		if item.kind == "" {
			return &ValidationError{Kind: ValidationInvalidOutput}
		}
		if index > 0 {
			if err := writeAll(writer, []byte{','}); err != nil {
				return err
			}
		}
		if err := writeCanonicalInputContentItem(writer, reader, item); err != nil {
			return err
		}
		index++
		return nil
	})
	if err != nil {
		return err
	}
	return writeAll(writer, []byte{']'})
}

func writeCanonicalInputContentItem(
	writer io.Writer,
	reader io.ReaderAt,
	item canonicalInputContentItem,
) error {
	if err := writeAll(writer, []byte(`{"type":`)); err != nil {
		return err
	}
	if err := writeJSONValue(writer, item.kind); err != nil {
		return err
	}
	writeSourceField := func(name string, slot int) error {
		if !item.hasValue(slot) {
			return nil
		}
		if err := writeAll(writer, []byte(`,"`+name+`":`)); err != nil {
			return err
		}
		if item.materialized {
			return writeJSONValue(writer, item.materializedValues[slot])
		}
		return writeJSONStringRange(writer, reader, item.values[slot], item.windows[slot])
	}
	switch item.kind {
	case "input_text":
		if err := writeSourceField("text", inputContentTextSlot); err != nil {
			return err
		}
	case "input_image":
		if err := writeSourceField("image_url", inputContentImageURLSlot); err != nil {
			return err
		}
		if item.detail != "" {
			if err := writeAll(writer, []byte(`,"detail":`)); err != nil {
				return err
			}
			if err := writeJSONValue(writer, item.detail); err != nil {
				return err
			}
		}
		if err := writeSourceField("file_id", inputContentFileIDSlot); err != nil {
			return err
		}
	case "input_file":
		for _, field := range []struct {
			name string
			slot int
		}{
			{"file_id", inputContentFileIDSlot},
			{"file_data", inputContentFileDataSlot},
			{"file_url", inputContentFileURLSlot},
			{"filename", inputContentFilenameSlot},
		} {
			if err := writeSourceField(field.name, field.slot); err != nil {
				return err
			}
		}
	default:
		return &ValidationError{Kind: ValidationInvalidOutput}
	}
	return writeAll(writer, []byte{'}'})
}

func writeJSONValue(writer io.Writer, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return writeAll(writer, bytes.TrimSpace(buffer.Bytes()))
}

func readJSONUnicodeEscape(reader io.Reader) (rune, error) {
	var encoded [4]byte
	if _, err := io.ReadFull(reader, encoded[:]); err != nil {
		return 0, err
	}
	var value rune
	for _, digit := range encoded {
		hex, ok := jsonHexValue(digit)
		if !ok {
			return 0, &ValidationError{Kind: ValidationInvalidOutput}
		}
		value = value<<4 | rune(hex)
	}
	return value, nil
}

func jsonHexValue(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func writeJSONStringFromReader(writer io.Writer, reader io.Reader) error {
	if err := writeAll(writer, []byte{'"'}); err != nil {
		return err
	}
	buffered := bufio.NewReader(reader)
	for {
		value, _, err := buffered.ReadRune()
		if errors.Is(err, io.EOF) {
			return writeAll(writer, []byte{'"'})
		}
		if err != nil {
			return err
		}
		if err := writeJSONEncodedRune(writer, value); err != nil {
			return err
		}
	}
}

func writeJSONEncodedRune(writer io.Writer, value rune) error {
	switch value {
	case '\\', '"':
		return writeAll(writer, []byte{'\\', byte(value)})
	case '\b':
		return writeAll(writer, []byte(`\b`))
	case '\f':
		return writeAll(writer, []byte(`\f`))
	case '\n':
		return writeAll(writer, []byte(`\n`))
	case '\r':
		return writeAll(writer, []byte(`\r`))
	case '\t':
		return writeAll(writer, []byte(`\t`))
	}
	if value < 0x20 || value == '\u2028' || value == '\u2029' {
		return writeAll(writer, []byte(fmt.Sprintf(`\u%04x`, value)))
	}
	if value == utf8.RuneError {
		value = '\uFFFD'
	}
	var encoded [utf8.UTFMax]byte
	size := utf8.EncodeRune(encoded[:], value)
	return writeAll(writer, encoded[:size])
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if written > 0 {
			payload = payload[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

type bytesJSONSource struct {
	value []byte
}

func (s bytesJSONSource) Size() int64 {
	return int64(len(s.value))
}

func (s bytesJSONSource) Open() (JSONSourceReader, error) {
	return nopReadAtCloser{Reader: bytes.NewReader(s.value)}, nil
}

type nopReadAtCloser struct {
	*bytes.Reader
}

func (nopReadAtCloser) Close() error {
	return nil
}

type heapScratchAllocator struct{}

func (heapScratchAllocator) Acquire(size int) ([]byte, func(), error) {
	if size <= 0 {
		return nil, nil, fmt.Errorf("OpenAI wire output scratch size must be positive: %d", size)
	}
	return make([]byte, size), func() {}, nil
}
