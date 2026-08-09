package serverapi
import ("encoding/json"; "testing")
func TestWorkspaceChatDraftOperationValidation(t *testing.T) {
	blank := ""; valid := []WorkspaceChatDraftOperation{{Kind: WorkspaceChatDraftReadMessage}, {Kind: WorkspaceChatDraftUpdateMessage, Message: &blank}, {Kind: WorkspaceChatDraftClear}, {Kind: WorkspaceChatDraftConsume}}
	for _, op := range valid { if err := op.Validate(); err != nil { t.Fatal(err) } }
	for _, op := range []WorkspaceChatDraftOperation{{Kind: "replace"}, {Kind: WorkspaceChatDraftOperationKind("update_message")}} { if err := op.Validate(); err == nil { t.Fatal("invalid operation accepted") } }
	if err := (WorkspaceChatDraftOperation{Kind: WorkspaceChatDraftReadMessage, Message: &blank}).Validate(); err == nil { t.Fatal("message accepted on read") }
	var request WorkspaceChatDraftRequest; if err := json.Unmarshal([]byte(`{"operation":{"kind":"read_message"},"settings":{}}`), &request); err == nil { t.Fatal("unknown field accepted") }
}
