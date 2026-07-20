package boundedjson

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

func (s *Scanner) ScanObject(
	knownFields KnownFields,
) (ScannedObject, error) {
	return s.ScanObjectFields(knownFields, nil)
}

func (s *Scanner) ScanObjectFields(
	knownFields KnownFields,
	visitField func(slot int, value Range) error,
) (ScannedObject, error) {
	if err := s.ensureOpen(); err != nil {
		return ScannedObject{}, err
	}
	if err := validateKnownFields(knownFields); err != nil {
		return ScannedObject{}, err
	}
	if err := s.skipWhitespace(); err != nil {
		return ScannedObject{}, err
	}
	out, err := s.scanKnownObjectValue(1, knownFields, visitField)
	if err != nil {
		return ScannedObject{}, err
	}
	return out, s.finishDocument()
}

func (s *Scanner) ScanObjectArray(
	knownFields KnownFields,
	visit func(index int, object ScannedObject) error,
) error {
	return s.ScanObjectArrayFields(knownFields, nil, visit)
}

func (s *Scanner) ScanObjectArrayFields(
	knownFields KnownFields,
	visitField func(index int, slot int, value Range) error,
	visit func(index int, object ScannedObject) error,
) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := validateKnownFields(knownFields); err != nil {
		return err
	}
	if visit == nil {
		return fmt.Errorf("migration JSON object-array visitor is required")
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if err := s.expectByte('['); err != nil {
		return err
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	next, err := s.requiredByte("array object or closing delimiter")
	if err != nil {
		return err
	}
	if next == ']' {
		s.offset++
		return s.finishDocument()
	}
	for index := 0; ; index++ {
		if next != '{' {
			return s.malformed("array object at offset %d", s.offset)
		}
		object, scanErr := s.scanKnownObjectValue(
			2,
			knownFields,
			func(slot int, value Range) error {
				if visitField == nil {
					return nil
				}
				return visitField(index, slot, value)
			},
		)
		if scanErr != nil {
			return scanErr
		}
		if visitErr := visit(index, object); visitErr != nil {
			return visitErr
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		next, err = s.requiredByte("array delimiter")
		if err != nil {
			return err
		}
		switch next {
		case ',':
			s.offset++
			if err := s.skipWhitespace(); err != nil {
				return err
			}
			next, err = s.requiredByte("array object")
			if err != nil {
				return err
			}
		case ']':
			s.offset++
			return s.finishDocument()
		default:
			return s.malformed("array delimiter at offset %d", s.offset)
		}
	}
}

func (s *Scanner) scanKnownObjectValue(
	depth int,
	knownFields KnownFields,
	visitField func(slot int, value Range) error,
) (ScannedObject, error) {
	out := ScannedObject{
		values:  make([]Range, len(knownFields)),
		present: make([]bool, len(knownFields)),
	}
	if err := s.scanObjectValue(depth, func(
		keyRange Range,
		valueRange Range,
	) error {
		slot, matchErr := s.matchKnownField(keyRange, knownFields)
		if matchErr != nil {
			return matchErr
		}
		if slot >= 0 {
			if visitField != nil {
				if err := visitField(slot, valueRange); err != nil {
					return err
				}
			}
			out.values[slot] = valueRange
			out.present[slot] = true
		}
		return nil
	}); err != nil {
		return ScannedObject{}, err
	}
	return out, nil
}

func (s *Scanner) ScanArray(
	visit func(index int, valueRange Range) error,
) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if visit == nil {
		return fmt.Errorf("migration JSON array visitor is required")
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if err := s.scanArrayValue(1, visit); err != nil {
		return err
	}
	return s.finishDocument()
}

func validateKnownFields(fields KnownFields) error {
	if len(fields) > MaxKnownFields {
		return fmt.Errorf(
			"migration known field count exceeds %d: %d",
			MaxKnownFields,
			len(fields),
		)
	}
	for index, field := range fields {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("migration known field %d is empty", index)
		}
		if len(field) > MaxKnownFieldNameBytes || !isKnownFieldName(field) {
			return fmt.Errorf(
				"migration known field %d is not a bounded ASCII field name",
				index,
			)
		}
		for previous := 0; previous < index; previous++ {
			if fields[previous] == field {
				return fmt.Errorf("migration known field %q is duplicated", field)
			}
		}
	}
	return nil
}

func (s *Scanner) finishDocument() error {
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if s.offset != s.end {
		return s.malformed("unexpected trailing JSON at offset %d", s.offset)
	}
	return nil
}

func (s *Scanner) scanValue(depth int) (Range, error) {
	if depth > MaxNesting {
		return Range{}, fmt.Errorf(
			"%w: maximum container depth %d exceeded at offset %d",
			ErrComplex,
			MaxNesting,
			s.offset,
		)
	}
	start := s.offset
	next, err := s.requiredByte("JSON value")
	if err != nil {
		return Range{}, err
	}
	switch next {
	case '"':
		return s.scanString()
	case '{':
		if err := s.scanObjectValue(depth+1, nil); err != nil {
			return Range{}, err
		}
	case '[':
		if err := s.scanArrayValue(depth+1, nil); err != nil {
			return Range{}, err
		}
	case 't':
		if err := s.consumeLiteral("true"); err != nil {
			return Range{}, err
		}
	case 'f':
		if err := s.consumeLiteral("false"); err != nil {
			return Range{}, err
		}
	case 'n':
		if err := s.consumeLiteral("null"); err != nil {
			return Range{}, err
		}
	default:
		if next != '-' && (next < '0' || next > '9') {
			return Range{}, s.malformed("JSON value at offset %d", s.offset)
		}
		if err := s.scanNumber(); err != nil {
			return Range{}, err
		}
	}
	return Range{Start: start, End: s.offset}, nil
}

func (s *Scanner) scanObjectValue(
	depth int,
	visit func(keyRange Range, valueRange Range) error,
) error {
	if depth > MaxNesting {
		return fmt.Errorf(
			"%w: maximum container depth %d exceeded at offset %d",
			ErrComplex,
			MaxNesting,
			s.offset,
		)
	}
	if err := s.expectByte('{'); err != nil {
		return err
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	next, err := s.requiredByte("object field or closing delimiter")
	if err != nil {
		return err
	}
	if next == '}' {
		s.offset++
		return nil
	}
	for {
		keyRange, err := s.scanString()
		if err != nil {
			return err
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		if err := s.expectByte(':'); err != nil {
			return err
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		valueRange, err := s.scanValue(depth)
		if err != nil {
			return err
		}
		if visit != nil {
			if err := visit(keyRange, valueRange); err != nil {
				return err
			}
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		delimiter, err := s.requiredByte("object delimiter")
		if err != nil {
			return err
		}
		switch delimiter {
		case ',':
			s.offset++
			if err := s.skipWhitespace(); err != nil {
				return err
			}
		case '}':
			s.offset++
			return nil
		default:
			return s.malformed("object delimiter at offset %d", s.offset)
		}
	}
}

func (s *Scanner) scanArrayValue(
	depth int,
	visit func(index int, valueRange Range) error,
) error {
	if depth > MaxNesting {
		return fmt.Errorf(
			"%w: maximum container depth %d exceeded at offset %d",
			ErrComplex,
			MaxNesting,
			s.offset,
		)
	}
	if err := s.expectByte('['); err != nil {
		return err
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	next, err := s.requiredByte("array element or closing delimiter")
	if err != nil {
		return err
	}
	if next == ']' {
		s.offset++
		return nil
	}
	for index := 0; ; index++ {
		valueRange, err := s.scanValue(depth)
		if err != nil {
			return err
		}
		if visit != nil {
			if err := visit(index, valueRange); err != nil {
				return err
			}
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		delimiter, err := s.requiredByte("array delimiter")
		if err != nil {
			return err
		}
		switch delimiter {
		case ',':
			s.offset++
			if err := s.skipWhitespace(); err != nil {
				return err
			}
		case ']':
			s.offset++
			return nil
		default:
			return s.malformed("array delimiter at offset %d", s.offset)
		}
	}
}

func (s *Scanner) scanString() (Range, error) {
	start := s.offset
	if err := s.expectByte('"'); err != nil {
		return Range{}, err
	}
	for {
		next, err := s.peekByte()
		if err != nil {
			return Range{}, s.malformed("unterminated string at offset %d", start)
		}
		s.offset++
		switch next {
		case '"':
			return Range{Start: start, End: s.offset}, nil
		case '\\':
			escaped, escapedErr := s.peekByte()
			if escapedErr != nil {
				return Range{}, s.malformed("unterminated escape at offset %d", s.offset)
			}
			s.offset++
			switch escaped {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				for count := 0; count < 4; count++ {
					hexByte, hexErr := s.peekByte()
					if hexErr != nil || !isJSONHex(hexByte) {
						return Range{}, s.malformed("invalid unicode escape at offset %d", s.offset)
					}
					s.offset++
				}
			default:
				return Range{}, s.malformed("invalid string escape at offset %d", s.offset-1)
			}
		default:
			if next < 0x20 {
				return Range{}, s.malformed("control byte in string at offset %d", s.offset-1)
			}
		}
	}
}

func (s *Scanner) scanNumber() error {
	start := s.offset
	next, err := s.peekByte()
	if err != nil {
		return err
	}
	if next == '-' {
		s.offset++
		next, err = s.peekByte()
		if err != nil {
			return s.malformed("incomplete number at offset %d", start)
		}
	}
	switch {
	case next == '0':
		s.offset++
		if following, followingErr := s.peekByte(); followingErr == nil && following >= '0' && following <= '9' {
			return s.malformed("leading zero in number at offset %d", start)
		}
	case next >= '1' && next <= '9':
		for {
			s.offset++
			next, err = s.peekByte()
			if err != nil || next < '0' || next > '9' {
				break
			}
		}
	default:
		return s.malformed("invalid number at offset %d", start)
	}
	if next == '.' {
		s.offset++
		digit, digitErr := s.peekByte()
		if digitErr != nil || digit < '0' || digit > '9' {
			return s.malformed("invalid fraction at offset %d", start)
		}
		for {
			s.offset++
			next, err = s.peekByte()
			if err != nil || next < '0' || next > '9' {
				break
			}
		}
	}
	if next == 'e' || next == 'E' {
		s.offset++
		next, err = s.peekByte()
		if err != nil {
			return s.malformed("invalid exponent at offset %d", start)
		}
		if next == '+' || next == '-' {
			s.offset++
			next, err = s.peekByte()
			if err != nil {
				return s.malformed("invalid exponent at offset %d", start)
			}
		}
		if next < '0' || next > '9' {
			return s.malformed("invalid exponent at offset %d", start)
		}
		for {
			s.offset++
			next, err = s.peekByte()
			if err != nil || next < '0' || next > '9' {
				break
			}
		}
	}
	return nil
}

func (s *Scanner) consumeLiteral(literal string) error {
	start := s.offset
	for index := range literal {
		next, err := s.peekByte()
		if err != nil || next != literal[index] {
			return s.malformed("invalid literal at offset %d", start)
		}
		s.offset++
	}
	return nil
}

func (s *Scanner) matchKnownField(
	keyRange Range,
	fields KnownFields,
) (int, error) {
	var decoded [MaxKnownFieldNameBytes]byte
	decodedLength, matchable, err := s.decodeKnownFieldName(keyRange, decoded[:])
	if err != nil {
		return -1, err
	}
	if !matchable {
		return -1, nil
	}
	for index, field := range fields {
		if len(field) == decodedLength &&
			(bytes.Equal(decoded[:decodedLength], []byte(field)) ||
				s.foldFieldNames && bytes.EqualFold(decoded[:decodedLength], []byte(field))) {
			return index, nil
		}
	}
	return -1, nil
}

func (s *Scanner) decodeKnownFieldName(
	keyRange Range,
	decoded []byte,
) (int, bool, error) {
	if keyRange.Size() < 2 {
		return 0, false, s.malformed("invalid object field name at offset %d", keyRange.Start)
	}
	decodedLength := 0
	for offset := keyRange.Start + 1; offset < keyRange.End-1; offset++ {
		current, err := s.byteAt(offset)
		if err != nil {
			return 0, false, fmt.Errorf("read migration JSON field name: %w", err)
		}
		if current >= 0x80 {
			return 0, false, nil
		}
		if current == '\\' {
			offset++
			escaped, escapedErr := s.byteAt(offset)
			if escapedErr != nil {
				return 0, false, fmt.Errorf("read migration JSON field escape: %w", escapedErr)
			}
			switch escaped {
			case '"', '\\', '/':
				current = escaped
			case 'b':
				current = '\b'
			case 'f':
				current = '\f'
			case 'n':
				current = '\n'
			case 'r':
				current = '\r'
			case 't':
				current = '\t'
			case 'u':
				var code uint16
				for count := 0; count < 4; count++ {
					offset++
					hexByte, hexErr := s.byteAt(offset)
					if hexErr != nil {
						return 0, false, fmt.Errorf("read migration JSON field unicode escape: %w", hexErr)
					}
					code = code<<4 | uint16(jsonHexValue(hexByte))
				}
				if code > 0x7f {
					return 0, false, nil
				}
				current = byte(code)
			default:
				return 0, false, s.malformed("invalid object field escape at offset %d", offset)
			}
		}
		if decodedLength >= len(decoded) {
			return 0, false, nil
		}
		decoded[decodedLength] = current
		decodedLength++
	}
	return decodedLength, true, nil
}

func (s *Scanner) skipWhitespace() error {
	for s.offset < s.end {
		next, err := s.peekByte()
		if err != nil {
			return err
		}
		switch next {
		case ' ', '\t', '\r', '\n':
			s.offset++
		default:
			return nil
		}
	}
	return nil
}

func (s *Scanner) expectByte(want byte) error {
	got, err := s.peekByte()
	if err != nil {
		return s.malformed("expected %q at offset %d", want, s.offset)
	}
	if got != want {
		return s.malformed("expected %q at offset %d", want, s.offset)
	}
	s.offset++
	return nil
}

func (s *Scanner) ensureOpen() error {
	if s == nil || s.closed {
		return ErrClosed
	}
	return nil
}

func (s *Scanner) requiredByte(context string) (byte, error) {
	next, err := s.peekByte()
	if errors.Is(err, io.EOF) {
		return 0, s.malformed("expected %s at offset %d", context, s.offset)
	}
	return next, err
}

func (s *Scanner) peekByte() (byte, error) {
	if err := s.ensureOpen(); err != nil {
		return 0, err
	}
	if s.offset >= s.end {
		return 0, io.EOF
	}
	if s.bufferStart < 0 ||
		s.offset < s.bufferStart ||
		s.offset >= s.bufferStart+int64(s.bufferLength) {
		if err := s.refill(); err != nil {
			return 0, err
		}
	}
	return s.buffer[s.offset-s.bufferStart], nil
}

func (s *Scanner) byteAt(offset int64) (byte, error) {
	originalOffset := s.offset
	s.offset = offset
	value, err := s.peekByte()
	s.offset = originalOffset
	return value, err
}

func (s *Scanner) refill() error {
	s.bufferStart = s.offset
	readLength := int64(len(s.buffer))
	if remaining := s.end - s.bufferStart; remaining < readLength {
		readLength = remaining
	}
	if readLength <= 0 {
		s.bufferLength = 0
		return io.EOF
	}
	n, err := s.source.ReadAt(s.buffer[:readLength], s.bufferStart)
	s.bufferLength = n
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read migration JSON at offset %d: %w", s.bufferStart, err)
	}
	if n == 0 {
		return io.EOF
	}
	return nil
}

func (s *Scanner) malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

func isJSONHex(value byte) bool {
	return (value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'f') ||
		(value >= 'A' && value <= 'F')
}

func jsonHexValue(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	default:
		return value - 'A' + 10
	}
}

func isKnownFieldName(field string) bool {
	for index := 0; index < len(field); index++ {
		value := field[index]
		if value < 0x20 || value >= 0x80 {
			return false
		}
	}
	return true
}
