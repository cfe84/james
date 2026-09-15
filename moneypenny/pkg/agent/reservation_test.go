package agent

import (
	"os/exec"
	"testing"
)

func TestRunnerReservationRejectsDuplicateAndPreservesReplacementOwner(t *testing.T) {
	r := New(nil)
	first := exec.Command("first")
	second := exec.Command("second")
	firstActivity := newActivityBuffer(1)
	secondActivity := newActivityBuffer(1)
	if !r.reserveProcess("session", first, firstActivity) {
		t.Fatal("first reservation failed")
	}
	if r.reserveProcess("session", second, secondActivity) {
		t.Fatal("duplicate reservation succeeded")
	}
	r.releaseProcess("session", second)
	if !r.IsRunning("session") {
		t.Fatal("non-owner cleanup removed active reservation")
	}

	r.releaseProcess("session", first)
	if !r.reserveProcess("session", second, secondActivity) {
		t.Fatal("replacement reservation failed")
	}
	r.releaseProcess("session", first)
	if !r.IsRunning("session") {
		t.Fatal("old owner cleanup removed replacement reservation")
	}
	if got := r.GetActivity("session"); got == nil {
		t.Fatal("old owner cleanup removed replacement activity")
	}
	r.releaseProcess("session", second)
	if r.IsRunning("session") {
		t.Fatal("owner cleanup did not release reservation")
	}
	if got := r.GetActivity("session"); got != nil {
		t.Fatal("current owner cleanup left activity")
	}
}
