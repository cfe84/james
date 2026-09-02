package handler

import "testing"

func TestSanitizeAttachmentNameCrossPlatformPaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "unix path", input: "/tmp/report.txt", want: "report.txt"},
		{name: "windows path", input: `C:\Users\alice\report.txt`, want: "report.txt"},
		{name: "mixed path", input: `..\..\report.txt`, want: "report.txt"},
		{name: "dot", input: ".", want: ""},
		{name: "dot dot", input: "..", want: ""},
		{name: "empty", input: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAttachmentName(tt.input); got != tt.want {
				t.Fatalf("sanitizeAttachmentName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
