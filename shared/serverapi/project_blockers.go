package serverapi

type ProjectWorkspaceUnlinkBlocker struct {
	Code  string `json:"code"`
	Count int    `json:"count,omitempty"`
}

type ProjectDeleteBlocker struct {
	Code  string `json:"code"`
	Count int    `json:"count,omitempty"`
}
