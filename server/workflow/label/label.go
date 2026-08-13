package label

import (
	"core/shared/labelcontract"
	"core/shared/runtimeids"
)

const (
	MaxNameRunes     = labelcontract.MaxNameRunes
	MaxProjectLabels = labelcontract.MaxProjectLabels
)

type ID = runtimeids.LabelID
type Name = labelcontract.Name
type NameErrorReason = labelcontract.NameErrorReason
type NameError = labelcontract.NameError

func Equal(left Name, right Name) bool {
	return labelcontract.Equal(left.String(), right.String())
}

func Contains(name Name, query string) bool {
	return labelcontract.Contains(name.String(), query)
}

func Compare(left Name, right Name) int {
	return labelcontract.Compare(left.String(), right.String())
}

const (
	NameErrorRequired         = labelcontract.NameErrorRequired
	NameErrorTooLong          = labelcontract.NameErrorTooLong
	NameErrorInvalidCharacter = labelcontract.NameErrorInvalidCharacter
)

var (
	NewID       = runtimeids.NewLabelID
	ParseID     = runtimeids.ParseLabelID
	PrepareName = labelcontract.PrepareName
)
