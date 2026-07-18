package clientui

type PromptAnswer struct {
	PromptID             *PromptID
	ErrorMessage         string
	Answer               string
	SelectedOptionNumber *int
	FreeformAnswer       string
	Approval             *ApprovalPromptAnswer
}

type ApprovalPromptAnswer struct {
	Decision   ApprovalDecision
	Commentary string
}
