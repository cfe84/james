package memory

import (
	"strings"
	"testing"
)

func TestReadImportedMemoryInUnicodePages(t *testing.T) {
	root := memoryRoot(t)
	body := strings.Repeat("🕴é\n", 24000)
	if err := Migrate(root, []*Node{{Path: "large", Body: body}}); err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	offset := 0
	for {
		page, err := Read(root, "large", offset, MaxReadCharacters)
		if err != nil || page == nil || page.Characters != 72000 {
			t.Fatalf("page: %+v, %v", page, err)
		}
		result.WriteString(page.Body)
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if result.String() != body {
		t.Fatal("Unicode pagination lost imported knowledge")
	}
	for _, args := range [][2]int{{-1, 1}, {0, 0}, {0, MaxReadCharacters + 1}, {72001, 1}} {
		if _, err := Read(root, "large", args[0], args[1]); err == nil {
			t.Fatalf("invalid page accepted: %v", args)
		}
	}
	oversized, err := Oversized(root)
	if err != nil || len(oversized) != 1 || oversized[0].Characters != 72000 || oversized[0].Path != "large" {
		t.Fatalf("wrong metadata-only sizes: %+v, %v", oversized, err)
	}
}

func TestReadPreservesEmbeddedNUL(t *testing.T) {
	root := memoryRoot(t)
	body := "a\x00" + strings.Repeat("é", 4001)
	if err := Migrate(root, []*Node{{Path: "", Body: body}}); err != nil {
		t.Fatal(err)
	}
	page, err := Read(root, "", 0, MaxReadCharacters)
	if err != nil || page == nil || page.Body != body || page.Characters != 4003 {
		t.Fatalf("NUL-containing note was truncated: %+v, %v", page, err)
	}
	sizes, err := Oversized(root)
	if err != nil || len(sizes) != 1 || sizes[0].Characters != 4003 {
		t.Fatalf("NUL-containing note missing from warning: %+v, %v", sizes, err)
	}
}
