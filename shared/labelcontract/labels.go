package labelcontract

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxNameRunes     = 64
	MaxProjectLabels = 100
)

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

const ComparisonVersion = "kent-label-casefold-v1"

func Fold(value string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(value)))
}

func Equal(left string, right string) bool {
	return Compare(left, right) == 0
}

func Contains(value string, query string) bool {
	return strings.Contains(Fold(value), Fold(query))
}

func Compare(left string, right string) int {
	return strings.Compare(Fold(left), Fold(right))
}

func validNameRune(character rune) bool {
	switch character {
	case ' ', ':', '&', '*', '%', '$', '#', '@', '!', '?', '.', ',', '/', '\\', '+', '|', '-', '_', '~', '\'':
		return true
	default:
		return unicode.IsLetter(character) || unicode.IsNumber(character)
	}
}
