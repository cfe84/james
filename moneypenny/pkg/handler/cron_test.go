package handler

import (
	"testing"
	"time"
)

func TestParseCronField(t *testing.T) {
	cases := []struct {
		s        string
		min, max int
		want     []int
	}{
		{"9,13,17", 0, 23, []int{9, 13, 17}},
		{"1-5", 0, 7, []int{1, 2, 3, 4, 5}},
		{"*/2", 0, 6, []int{0, 2, 4, 6}},
		{"0-10/5", 0, 59, []int{0, 5, 10}},
		{"*", 1, 3, []int{1, 2, 3}},
		{"17,9", 0, 23, []int{9, 17}},
		{"7", 0, 7, []int{7}},
	}
	for _, c := range cases {
		got, err := parseCronField(c.s, c.min, c.max)
		if err != nil {
			t.Fatalf("%q: unexpected err %v", c.s, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%q: got %v want %v", c.s, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: got %v want %v", c.s, got, c.want)
			}
		}
	}
}

func TestNextCronTixie(t *testing.T) {
	// Tue 2026-08-18 17:00 local -> next should be Wed 2026-08-19 09:00 local.
	after := time.Date(2026, 8, 18, 17, 0, 0, 0, time.Local)
	next, err := nextCronTime("0 9,13,17 * * 1-5", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 19, 9, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("got %s want %s", next, want)
	}
}

func TestNextCronSundayAs7(t *testing.T) {
	// Sat 2026-08-22 -> "0 0 * * 7" (Sunday) should be Sun 2026-08-23 00:00.
	after := time.Date(2026, 8, 22, 12, 0, 0, 0, time.Local)
	next, err := nextCronTime("0 0 * * 7", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 23, 0, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("got %s want %s", next, want)
	}
}
