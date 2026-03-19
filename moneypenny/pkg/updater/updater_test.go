package updater

import "testing"

func TestIsNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.10.3", "0.10.2", true},
		{"0.10.2", "0.10.2", false},
		{"0.10.1", "0.10.2", false},
		{"0.11.0", "0.10.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.10.2", "0.10.3", false},
	}
	for _, tt := range tests {
		got := isNewer(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  [3]int
	}{
		{"0.10.2", [3]int{0, 10, 2}},
		{"v1.2.3", [3]int{1, 2, 3}},
		{"0.0.1", [3]int{0, 0, 1}},
	}
	for _, tt := range tests {
		got := parseVersion(tt.input)
		if got != tt.want {
			t.Errorf("parseVersion(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

type mockChecker struct {
	idle bool
}

func (m *mockChecker) AllSessionsIdle() bool { return m.idle }

func TestStatusInitial(t *testing.T) {
	u := New("0.10.2", "cfe84/james", "/tmp/test", &mockChecker{idle: true})
	info := u.Status()
	if info.CurrentVersion != "0.10.2" {
		t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, "0.10.2")
	}
	if info.Status != StatusUpToDate {
		t.Errorf("Status = %q, want %q", info.Status, StatusUpToDate)
	}
	if info.UpdateAvailable {
		t.Errorf("UpdateAvailable should be false initially")
	}
}
