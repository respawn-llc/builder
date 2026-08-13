package metadata

func testDraft() WorkspaceChatDraftDocument {
	return WorkspaceChatDraftDocument{Message: "verbatim\nmessage", Agent: "default", Supervisor: "edits", Thinking: "medium", Fast: true, Questions: false, AutoCompaction: true}
}
