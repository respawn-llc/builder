package lifecyclehook

import "core/shared/lifecyclecontract"

type Issue struct {
	Category lifecyclecontract.Category
	Err      error
	Stderr   string
}
