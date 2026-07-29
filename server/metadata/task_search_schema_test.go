package metadata

import (
	"testing"
)

func TestTaskSearchSchemaExposesTheRequiredOperationalContract(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	objects, err := store.Queries().ListTaskSearchSchemaObjects(t.Context())
	if err != nil {
		t.Fatalf("ListTaskSearchSchemaObjects: %v", err)
	}
	got := make(map[string]bool, len(objects))
	for _, object := range objects {
		got[object.ObjectKind+":"+object.ObjectName] = true
	}
	for _, want := range []string{
		"table:task_search_documents",
		"table:task_search_fts",
		"view:task_search_content",
		"trigger:task_search_document_insert",
		"trigger:task_search_document_delete",
		"trigger:task_search_task_insert",
		"trigger:task_search_comment_insert",
		"trigger:task_search_task_title_before_update",
		"trigger:task_search_task_title_after_update",
		"trigger:task_search_task_body_before_update",
		"trigger:task_search_task_body_after_update",
		"trigger:task_search_comment_body_before_update",
		"trigger:task_search_comment_body_after_update",
		"trigger:task_search_comment_delete",
		"trigger:task_search_task_delete",
	} {
		if !got[want] {
			t.Fatalf("task-search schema objects = %v, missing %q", got, want)
		}
	}
	if _, err := store.Queries().CheckTaskSearchSchemaContract(t.Context()); err != nil {
		t.Fatalf("CheckTaskSearchSchemaContract: %v", err)
	}
}
