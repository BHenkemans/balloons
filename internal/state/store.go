// Package state persists per-balloon ticket lifecycle in a local SQLite file:
// when the printer accepted the ticket (printed_at) and when the balloon was
// marked done in DOMjudge through our MarkDone RPC (delivered_at). It is the
// source of truth for "have we already printed this balloon?", which lets the
// hub safely reprint nothing on restart and still catch balloons inserted
// while the server was down.
package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Record struct {
	BalloonID   int64
	PrintedAt   *time.Time
	DeliveredAt *time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS ticket_state (
    balloon_id   INTEGER PRIMARY KEY,
    printed_at   TEXT,
    delivered_at TEXT
);

CREATE TABLE IF NOT EXISTS runners (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    session_token   TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    last_seen_at    TEXT NOT NULL,
    available_since TEXT
);

CREATE TABLE IF NOT EXISTS assignments (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    balloon_id       INTEGER NOT NULL,
    runner_id        INTEGER NOT NULL,
    state            TEXT NOT NULL,
    assigned_at      TEXT NOT NULL,
    delivered_at     TEXT,
    cancelled_at     TEXT,
    cancelled_reason TEXT,
    FOREIGN KEY (runner_id) REFERENCES runners(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_balloon ON assignments (balloon_id) WHERE state = 'assigned';
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_runner ON assignments (runner_id) WHERE state = 'assigned';
CREATE INDEX IF NOT EXISTS idx_assignments_runner ON assignments (runner_id);
`

// Open opens (or creates) the SQLite database at path and ensures the schema
// exists. The returned Store is safe for concurrent use.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("state: open %q: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state: schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// IsPrinted reports whether ticket_state has a non-null printed_at for this id.
func (s *Store) IsPrinted(balloonID int64) (bool, error) {
	var has int
	err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM ticket_state WHERE balloon_id=? AND printed_at IS NOT NULL)`,
		balloonID,
	).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("state: IsPrinted(%d): %w", balloonID, err)
	}
	return has == 1, nil
}

// RecordPrinted sets printed_at = now() for this balloon, creating the row if
// it doesn't exist. Idempotent re-calls overwrite the timestamp; that's fine
// because IsPrinted gates upstream so we only get here for a fresh print.
func (s *Store) RecordPrinted(balloonID int64) error {
	_, err := s.db.Exec(
		`INSERT INTO ticket_state (balloon_id, printed_at) VALUES (?, ?)
		 ON CONFLICT(balloon_id) DO UPDATE SET printed_at=excluded.printed_at`,
		balloonID, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("state: RecordPrinted(%d): %w", balloonID, err)
	}
	return nil
}

// ClearPrinted nulls printed_at for this balloon so the next print attempt is
// allowed through. Used to support manual "reprint" requests for tickets that
// were lost in transit. No-op if the row doesn't exist.
func (s *Store) ClearPrinted(balloonID int64) error {
	_, err := s.db.Exec(`UPDATE ticket_state SET printed_at=NULL WHERE balloon_id=?`, balloonID)
	if err != nil {
		return fmt.Errorf("state: ClearPrinted(%d): %w", balloonID, err)
	}
	return nil
}

// RecordDelivered sets delivered_at = now(), creating the row if needed. Called
// after a successful MarkDone against DOMjudge.
func (s *Store) RecordDelivered(balloonID int64) error {
	_, err := s.db.Exec(
		`INSERT INTO ticket_state (balloon_id, delivered_at) VALUES (?, ?)
		 ON CONFLICT(balloon_id) DO UPDATE SET delivered_at=excluded.delivered_at`,
		balloonID, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("state: RecordDelivered(%d): %w", balloonID, err)
	}
	return nil
}

// Get returns the ticket_state row for balloonID. The second return value is
// false if no row exists. Useful for diagnostics; not on the hot path.
func (s *Store) Get(balloonID int64) (Record, bool, error) {
	var r Record
	r.BalloonID = balloonID
	var printed, delivered sql.NullString
	err := s.db.QueryRow(
		`SELECT printed_at, delivered_at FROM ticket_state WHERE balloon_id=?`,
		balloonID,
	).Scan(&printed, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("state: Get(%d): %w", balloonID, err)
	}
	if printed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, printed.String)
		r.PrintedAt = &t
	}
	if delivered.Valid {
		t, _ := time.Parse(time.RFC3339Nano, delivered.String)
		r.DeliveredAt = &t
	}
	return r, true, nil
}
