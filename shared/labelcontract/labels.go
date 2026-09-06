package labelcontract

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MaxProjectLabels = 100

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
