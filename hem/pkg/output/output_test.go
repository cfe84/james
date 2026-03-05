package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]string{"name": "alice", "role": "admin"}

	if err := Print(&buf, FormatJSON, data); err != nil {
		t.Fatalf("Print JSON: %v", err)
	}

	out := buf.String()

	// Must be valid JSON.
	var parsed map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}

	// Must be indented (contains newlines within the JSON).
	if !strings.Contains(out, "\n  ") {
		t.Errorf("expected indented JSON, got: %s", out)
	}

	if parsed["name"] != "alice" || parsed["role"] != "admin" {
		t.Errorf("unexpected parsed values: %v", parsed)
	}
}

func TestPrintTextTableData(t *testing.T) {
	var buf bytes.Buffer
	td := TableData{
		Headers: []string{"Name", "Age"},
		Rows: [][]string{
			{"Alice", "30"},
			{"Bob", "25"},
		},
	}

	if err := Print(&buf, FormatText, td); err != nil {
		t.Fatalf("Print Text: %v", err)
	}

	out := buf.String()

	if !strings.Contains(out, "Name: Alice") {
		t.Errorf("expected 'Name: Alice' in output: %s", out)
	}
	if !strings.Contains(out, "Age: 30") {
		t.Errorf("expected 'Age: 30' in output: %s", out)
	}
	if !strings.Contains(out, "Name: Bob") {
		t.Errorf("expected 'Name: Bob' in output: %s", out)
	}

	// Rows should be separated by blank lines.
	if !strings.Contains(out, "\n\n") {
		t.Errorf("expected blank line between rows in output: %s", out)
	}
}

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	td := TableData{
		Headers: []string{"Name", "Age"},
		Rows: [][]string{
			{"Alice", "30"},
			{"Bob", "25"},
		},
	}

	if err := Print(&buf, FormatTable, td); err != nil {
		t.Fatalf("Print Table: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2 rows), got %d: %s", len(lines), out)
	}

	// Header line should contain both headers.
	if !strings.Contains(lines[0], "Name") || !strings.Contains(lines[0], "Age") {
		t.Errorf("header line missing headers: %s", lines[0])
	}

	if !strings.Contains(lines[1], "Alice") || !strings.Contains(lines[1], "30") {
		t.Errorf("first data row missing values: %s", lines[1])
	}
}

func TestPrintTSV(t *testing.T) {
	var buf bytes.Buffer
	td := TableData{
		Headers: []string{"Name", "Age"},
		Rows: [][]string{
			{"Alice", "30"},
			{"Bob", "25"},
		},
	}

	if err := Print(&buf, FormatTSV, td); err != nil {
		t.Fatalf("Print TSV: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), out)
	}

	// Header should be tab-separated.
	if lines[0] != "Name\tAge" {
		t.Errorf("expected tab-separated header, got: %q", lines[0])
	}

	if lines[1] != "Alice\t30" {
		t.Errorf("expected tab-separated row, got: %q", lines[1])
	}

	if lines[2] != "Bob\t25" {
		t.Errorf("expected tab-separated row, got: %q", lines[2])
	}
}

func TestPrintTextString(t *testing.T) {
	var buf bytes.Buffer

	if err := Print(&buf, FormatText, "hello world"); err != nil {
		t.Fatalf("Print Text string: %v", err)
	}

	out := strings.TrimSpace(buf.String())
	if out != "hello world" {
		t.Errorf("expected 'hello world', got: %q", out)
	}
}

func TestTextFunc(t *testing.T) {
	var buf bytes.Buffer
	Text(&buf, "simple message")

	out := strings.TrimSpace(buf.String())
	if out != "simple message" {
		t.Errorf("expected 'simple message', got: %q", out)
	}
}
