package label

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"core/shared/labelcontract"
	"core/shared/runtimeids"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxNameRunes     = 64
	MaxProjectLabels = labelcontract.MaxProjectLabels
)

type ID struct {
	value uuid.UUID
}

func NewID() ID {
	return ID{value: uuid.New()}
}

func ParseID(raw string) (ID, error) {
	parsed, err := runtimeids.ParseCanonicalUUIDv4(raw, "label ID")
	if err != nil {
		return ID{}, err
	}
	return ID{value: parsed}, nil
}

func (id ID) String() string {
	return id.value.String()
}

type Name string

type NameErrorReason string

const (
	NameErrorRequired         NameErrorReason = "required"
	NameErrorTooLong          NameErrorReason = "too_long"
	NameErrorInvalidCharacter NameErrorReason = "invalid_character"
)

type NameError struct {
	Reason NameErrorReason
	Rune   *rune
}

func (err *NameError) Error() string {
	switch err.Reason {
	case NameErrorRequired:
		return "label name is required"
	case NameErrorTooLong:
		return fmt.Sprintf("label name must be at most %d characters", MaxNameRunes)
	case NameErrorInvalidCharacter:
		if err.Rune == nil {
			return "label name contains an invalid character"
		}
		return fmt.Sprintf("label name contains invalid character %q", *err.Rune)
	default:
		return "label name is invalid"
	}
}

func PrepareName(raw string) (Name, error) {
	prepared := norm.NFC.String(strings.TrimSpace(raw))
	if prepared == "" {
		return "", &NameError{Reason: NameErrorRequired}
	}
	if utf8.RuneCountInString(prepared) > MaxNameRunes {
		return "", &NameError{Reason: NameErrorTooLong}
	}
	for _, character := range prepared {
		if validNameRune(character) {
			continue
		}
		invalidCharacter := character
		return "", &NameError{
			Reason: NameErrorInvalidCharacter,
			Rune:   &invalidCharacter,
		}
	}
	return Name(prepared), nil
}

func (name Name) String() string {
	return string(name)
}

func Equal(left Name, right Name) bool {
	return labelcontract.Equal(left.String(), right.String())
}

func Contains(name Name, query string) bool {
	return labelcontract.Contains(name.String(), query)
}

func Compare(left Name, right Name) int {
	return labelcontract.Compare(left.String(), right.String())
}

func validNameRune(character rune) bool {
	switch character {
	case ' ', ':', '&', '*', '%', '$', '#', '@', '!', '?', '.', ',', '/', '\\', '+', '|', '-', '_', '~', '\'':
		return true
	default:
		return unicode.IsLetter(character) || unicode.IsNumber(character)
	}
}
