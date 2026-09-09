package ui

import "testing"

func TestMemoryEditorAcceptsExistingRoot(t *testing.T) {
	model := memoryModel{isNew: false}
	model.fields, model.original = newEditFields("", "# Root\nTopic index", false)
	updated, cmd := model.submitEdit()
	if updated.err != nil || cmd == nil || !updated.saving {
		t.Fatalf("root cannot be saved: %v", updated.err)
	}
	model.isNew = true
	updated, cmd = model.submitEdit()
	if updated.err == nil || cmd != nil {
		t.Fatal("new non-root node accepted an empty path")
	}
}
