package state

import (
	"path/filepath"
	"testing"
)

func TestStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const id int64 = 123

	if printed, err := s.IsPrinted(id); err != nil || printed {
		t.Fatalf("initial IsPrinted: got (%v, %v), want (false, nil)", printed, err)
	}

	if err := s.RecordPrinted(id); err != nil {
		t.Fatalf("RecordPrinted: %v", err)
	}
	if printed, err := s.IsPrinted(id); err != nil || !printed {
		t.Fatalf("after RecordPrinted: IsPrinted got (%v, %v), want (true, nil)", printed, err)
	}

	if err := s.RecordDelivered(id); err != nil {
		t.Fatalf("RecordDelivered: %v", err)
	}

	r, ok, err := s.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get: got (%+v, %v, %v), want a row and no error", r, ok, err)
	}
	if r.PrintedAt == nil || r.DeliveredAt == nil {
		t.Fatalf("expected both timestamps set, got printed=%v delivered=%v", r.PrintedAt, r.DeliveredAt)
	}
}
