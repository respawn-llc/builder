package session

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
)

type eventRecordStreamInspection struct {
	Sequence          int64
	Kind              EventKind
	QuestionCandidate bool
}

func inspectEventRecordStream(
	reader io.Reader,
	version int,
) (eventRecordStreamInspection, error) {
	if version != EventLogVersionV1 && version != EventLogVersionV2 {
		return eventRecordStreamInspection{}, fmt.Errorf(
			"unsupported event log version %d",
			version,
		)
	}
	scanner := jsonStreamScanner{reader: bufio.NewReaderSize(reader, int(eventLogScanChunkSize))}
	var inspection eventRecordStreamInspection
	var sequencePresent bool
	var kindPresent bool
	var toolNamePresent bool
	var toolNameIsQuestion bool
	var toolErrorPresent bool
	var toolIsError bool
	var questionAnswerPresent bool
	err := scanner.scanObject(func(field string) error {
		switch field {
		case "seq":
			value, err := scanner.scanInt64()
			if err != nil {
				return fmt.Errorf("decode event sequence: %w", err)
			}
			inspection.Sequence = value
			sequencePresent = true
		case "kind":
			value, err := scanner.scanString(256)
			if err != nil {
				return fmt.Errorf("decode event kind: %w", err)
			}
			inspection.Kind = EventKind(value)
			kindPresent = true
		case "payload":
			if err := scanner.scanObject(func(payloadField string) error {
				switch payloadField {
				case "name":
					value, err := scanner.scanString(256)
					if err != nil {
						return err
					}
					toolNamePresent = true
					toolNameIsQuestion = value == askQuestionToolName
				case "is_error":
					value, err := scanner.scanBool()
					if err != nil {
						return err
					}
					toolErrorPresent = true
					toolIsError = value
				case "question_answer":
					next, err := scanner.peekNonSpace()
					if err != nil {
						return err
					}
					questionAnswerPresent = next != 'n'
					return scanner.skipValue()
				default:
					return scanner.skipValue()
				}
				return nil
			}); err != nil {
				return fmt.Errorf("decode event payload: %w", err)
			}
		default:
			return scanner.skipValue()
		}
		return nil
	})
	if err != nil {
		return eventRecordStreamInspection{}, err
	}
	if err := scanner.requireEOF(); err != nil {
		return eventRecordStreamInspection{}, err
	}
	if !sequencePresent || inspection.Sequence <= 0 {
		return eventRecordStreamInspection{}, fmt.Errorf(
			"event sequence must be positive: %d",
			inspection.Sequence,
		)
	}
	if !kindPresent {
		return eventRecordStreamInspection{}, errors.New("event kind is required")
	}
	if err := validateEventKind(inspection.Kind); err != nil {
		return eventRecordStreamInspection{}, err
	}
	if inspection.Kind != EventKindToolCompletion {
		return inspection, nil
	}
	if !toolNamePresent || !toolErrorPresent {
		return eventRecordStreamInspection{}, errors.New(
			"tool completion name and is_error are required",
		)
	}
	inspection.QuestionCandidate = toolNameIsQuestion && !toolIsError
	if version == EventLogVersionV2 {
		toolName := ""
		if toolNameIsQuestion {
			toolName = askQuestionToolName
		}
		if !toolNameIsQuestion {
			toolName = "non_question_tool"
		}
		if err := validateV2QuestionAnswerPlacement(
			toolName,
			toolIsError,
			questionAnswerPresent,
		); err != nil {
			return eventRecordStreamInspection{}, err
		}
	}
	return inspection, nil
}

type jsonStreamScanner struct {
	reader *bufio.Reader
}

func (s *jsonStreamScanner) scanObject(field func(string) error) error {
	if err := s.expect('{'); err != nil {
		return err
	}
	if next, err := s.peekNonSpace(); err != nil {
		return err
	} else if next == '}' {
		_, _ = s.reader.ReadByte()
		return nil
	}
	for {
		name, err := s.scanString(256)
		if err != nil {
			return fmt.Errorf("decode object field name: %w", err)
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := field(name); err != nil {
			return err
		}
		next, err := s.readNonSpace()
		if err != nil {
			return err
		}
		switch next {
		case '}':
			return nil
		case ',':
		default:
			return fmt.Errorf("expected object delimiter, got %q", next)
		}
	}
}

func (s *jsonStreamScanner) skipValue() error {
	next, err := s.peekNonSpace()
	if err != nil {
		return err
	}
	switch next {
	case '{':
		return s.scanObject(func(string) error { return s.skipValue() })
	case '[':
		return s.skipArray()
	case '"':
		return s.skipString()
	case 't':
		return s.expectLiteral("true")
	case 'f':
		return s.expectLiteral("false")
	case 'n':
		return s.expectLiteral("null")
	default:
		return s.skipNumber()
	}
}

func (s *jsonStreamScanner) skipArray() error {
	if err := s.expect('['); err != nil {
		return err
	}
	if next, err := s.peekNonSpace(); err != nil {
		return err
	} else if next == ']' {
		_, _ = s.reader.ReadByte()
		return nil
	}
	for {
		if err := s.skipValue(); err != nil {
			return err
		}
		next, err := s.readNonSpace()
		if err != nil {
			return err
		}
		switch next {
		case ']':
			return nil
		case ',':
		default:
			return fmt.Errorf("expected array delimiter, got %q", next)
		}
	}
}

func (s *jsonStreamScanner) scanString(limit int) (string, error) {
	raw := make([]byte, 0, 32)
	err := s.scanStringBytes(func(value byte) error {
		if len(raw) >= limit {
			return fmt.Errorf("JSON string exceeds %d-byte inspection limit", limit)
		}
		raw = append(raw, value)
		return nil
	})
	if err != nil {
		return "", err
	}
	decoded, err := strconv.Unquote(`"` + string(raw) + `"`)
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func (s *jsonStreamScanner) skipString() error {
	return s.scanStringBytes(func(byte) error { return nil })
}

func (s *jsonStreamScanner) scanStringBytes(visit func(byte) error) error {
	if err := s.expect('"'); err != nil {
		return err
	}
	escaped := false
	unicodeDigits := 0
	for {
		value, err := s.reader.ReadByte()
		if err != nil {
			return err
		}
		if unicodeDigits > 0 {
			if !isJSONHex(value) {
				return fmt.Errorf("invalid JSON unicode escape byte %q", value)
			}
			unicodeDigits--
			if err := visit(value); err != nil {
				return err
			}
			continue
		}
		if escaped {
			escaped = false
			if value == 'u' {
				unicodeDigits = 4
			} else if !isJSONEscape(value) {
				return fmt.Errorf("invalid JSON escape byte %q", value)
			}
			if err := visit(value); err != nil {
				return err
			}
			continue
		}
		switch {
		case value == '"':
			return nil
		case value == '\\':
			escaped = true
		case value < 0x20:
			return fmt.Errorf("invalid control byte in JSON string: %d", value)
		}
		if err := visit(value); err != nil {
			return err
		}
	}
}

func (s *jsonStreamScanner) scanBool() (bool, error) {
	next, err := s.peekNonSpace()
	if err != nil {
		return false, err
	}
	switch next {
	case 't':
		return true, s.expectLiteral("true")
	case 'f':
		return false, s.expectLiteral("false")
	default:
		return false, fmt.Errorf("expected JSON boolean, got %q", next)
	}
}

func (s *jsonStreamScanner) scanInt64() (int64, error) {
	raw, err := s.scanNumber(64)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(raw, 10, 64)
}

func (s *jsonStreamScanner) skipNumber() error {
	_, err := s.scanNumber(64)
	return err
}

func (s *jsonStreamScanner) scanNumber(limit int) (string, error) {
	raw := make([]byte, 0, 24)
	for {
		next, err := s.reader.Peek(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if len(next) == 0 || !isJSONNumberByte(next[0]) {
			break
		}
		value, _ := s.reader.ReadByte()
		if len(raw) >= limit {
			return "", fmt.Errorf("JSON number exceeds %d-byte inspection limit", limit)
		}
		raw = append(raw, value)
	}
	if len(raw) == 0 {
		return "", errors.New("JSON number is required")
	}
	value := string(raw)
	if !validJSONNumber(value) {
		return "", fmt.Errorf("invalid JSON number %q", value)
	}
	return value, nil
}

func (s *jsonStreamScanner) requireEOF() error {
	if _, err := s.readNonSpace(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("unexpected trailing bytes after JSON value")
}

func (s *jsonStreamScanner) expect(want byte) error {
	value, err := s.readNonSpace()
	if err != nil {
		return err
	}
	if value != want {
		return fmt.Errorf("expected %q, got %q", want, value)
	}
	return nil
}

func (s *jsonStreamScanner) expectLiteral(want string) error {
	for index := range len(want) {
		value, err := s.reader.ReadByte()
		if err != nil {
			return err
		}
		if value != want[index] {
			return fmt.Errorf("expected JSON literal %q", want)
		}
	}
	return nil
}

func (s *jsonStreamScanner) peekNonSpace() (byte, error) {
	if err := s.skipSpace(); err != nil {
		return 0, err
	}
	value, err := s.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (s *jsonStreamScanner) readNonSpace() (byte, error) {
	if err := s.skipSpace(); err != nil {
		return 0, err
	}
	return s.reader.ReadByte()
}

func (s *jsonStreamScanner) skipSpace() error {
	for {
		value, err := s.reader.Peek(1)
		if err != nil {
			return err
		}
		switch value[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = s.reader.ReadByte()
		default:
			return nil
		}
	}
}

func isJSONEscape(value byte) bool {
	switch value {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	default:
		return false
	}
}

func isJSONHex(value byte) bool {
	return value >= '0' && value <= '9' ||
		value >= 'a' && value <= 'f' ||
		value >= 'A' && value <= 'F'
}

func isJSONNumberByte(value byte) bool {
	return value >= '0' && value <= '9' ||
		value == '-' || value == '+' || value == '.' ||
		value == 'e' || value == 'E'
}

func validJSONNumber(value string) bool {
	index := 0
	if value[index] == '-' {
		index++
		if index == len(value) {
			return false
		}
	}
	if value[index] == '0' {
		index++
	} else {
		if value[index] < '1' || value[index] > '9' {
			return false
		}
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
	}
	if index < len(value) && value[index] == '.' {
		index++
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		index++
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			index++
		}
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	return index == len(value)
}
