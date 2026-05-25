package state

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestActiveAssignmentUniquePerRunner pins the schema invariant that a single
// runner can hold at most one `assigned`-state assignment. Without this, a
// failed SetRunnerStatus during dispatch could leave an orphan that lets the
// same runner get paired with a second balloon on the next pass.
func TestActiveAssignmentUniquePerRunner(t *testing.T) {
	s := openTestStore(t)

	r, err := s.CreateRunner("alice")
	if err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	if _, err := s.CreateAssignment(100, r.ID); err != nil {
		t.Fatalf("first CreateAssignment: %v", err)
	}
	_, err = s.CreateAssignment(101, r.ID)
	if err == nil {
		t.Fatalf("second CreateAssignment for same runner unexpectedly succeeded — uq_active_runner missing?")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE constraint failure, got: %v", err)
	}
}

// TestActiveAssignmentUniquePerBalloon pins the existing partial unique index
// on (balloon_id) WHERE state='assigned' — the dispatcher relies on it to
// dedupe re-dispatch attempts for the same balloon.
func TestActiveAssignmentUniquePerBalloon(t *testing.T) {
	s := openTestStore(t)

	r1, _ := s.CreateRunner("alice")
	r2, _ := s.CreateRunner("bob")

	if _, err := s.CreateAssignment(200, r1.ID); err != nil {
		t.Fatalf("first CreateAssignment: %v", err)
	}
	_, err := s.CreateAssignment(200, r2.ID)
	if err == nil {
		t.Fatalf("second CreateAssignment for same balloon unexpectedly succeeded")
	}
}

// TestActiveAssignmentReusableAfterCancel verifies that cancelling an
// assignment frees up both the runner and the balloon for new dispatch — this
// is the rollback path used when SetRunnerStatus fails mid-dispatch.
func TestActiveAssignmentReusableAfterCancel(t *testing.T) {
	s := openTestStore(t)

	r, _ := s.CreateRunner("alice")
	a, err := s.CreateAssignment(300, r.ID)
	if err != nil {
		t.Fatalf("CreateAssignment: %v", err)
	}
	if err := s.CancelAssignment(a.ID, CancelReasonDispatchFailed); err != nil {
		t.Fatalf("CancelAssignment: %v", err)
	}
	if _, err := s.CreateAssignment(300, r.ID); err != nil {
		t.Fatalf("re-CreateAssignment after cancel: %v", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
