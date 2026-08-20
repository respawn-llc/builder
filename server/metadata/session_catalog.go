package metadata

import (
	"core/shared/clientui"
	"core/shared/sessioncontract"
)

const MaxSessionPageSize = 100

type SessionPage struct {
	ProjectID  string
	Category   sessioncontract.SessionCategory
	Sessions   []clientui.SessionSummary
	NextOffset *int
}
