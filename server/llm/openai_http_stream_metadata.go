package llm

import (
	"strings"
)

type responseStreamMetadata struct{ standardServedModel *string }

func (m *responseStreamMetadata) ObserveStandardModel(model string) {
	if m == nil || m.standardServedModel != nil {
		return
	}
	if model = strings.TrimSpace(model); model != "" {
		m.standardServedModel = &model
	}
}
