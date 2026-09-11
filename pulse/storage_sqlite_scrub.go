package pulse

import (
	"encoding/json"
	"time"
)

// One-time cleanup of error records written by Pulse v1.0.0, which stored
// truncated JSON bodies, non-JSON bodies and query strings unredacted. The
// migration re-applies redaction to every stored error once per database
// and records that it ran in pulse_meta.

const scrubMigrationKey = "scrub_v1"

// errorScrubber is implemented by storage backends that persist error
// records across versions (SQLite). Unexported for the same reason as
// rollupStore.
type errorScrubber interface {
	scrubStoredErrors(r *redactor) (int, error)
}

var _ errorScrubber = (*SQLiteStorage)(nil)

// scrubStoredErrors re-redacts every stored error record, unless this
// database has already been scrubbed. It returns how many records changed.
func (s *SQLiteStorage) scrubStoredErrors(r *redactor) (int, error) {
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var done int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pulse_meta WHERE key = ?`, scrubMigrationKey).Scan(&done); err != nil {
		return 0, err
	}
	if done > 0 {
		return 0, nil
	}

	type row struct{ fingerprint, message, ctx string }
	rows, err := s.db.Query(`SELECT fingerprint, error_message, request_ctx FROM errors`)
	if err != nil {
		return 0, err
	}
	var all []row
	for rows.Next() {
		var rw row
		if err := rows.Scan(&rw.fingerprint, &rw.message, &rw.ctx); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, rw)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // no-op once committed

	changed := 0
	for _, rw := range all {
		msg := r.scrubValue(rw.message)
		ctx := rw.ctx
		if ctx != "" && ctx != "null" {
			var rc RequestContext
			if err := json.Unmarshal([]byte(ctx), &rc); err == nil {
				r.rescrub(&rc)
				if b, err := json.Marshal(rc); err == nil {
					ctx = string(b)
				}
			} else {
				ctx = "" // unreadable: drop rather than keep possibly-sensitive text
			}
		}
		if msg == rw.message && ctx == rw.ctx {
			continue
		}
		if _, err := tx.Exec(`UPDATE errors SET error_message = ?, request_ctx = ? WHERE fingerprint = ?`,
			msg, ctx, rw.fingerprint); err != nil {
			return 0, err
		}
		changed++
	}
	if _, err := tx.Exec(`INSERT INTO pulse_meta (key, value) VALUES (?, ?)`,
		scrubMigrationKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	return changed, tx.Commit()
}

// scrubStoredErrors runs the one-time error scrub on backends that need it.
func scrubStoredErrors(p *Pulse) {
	sc, ok := p.storage.(errorScrubber)
	if !ok {
		return
	}
	n, err := sc.scrubStoredErrors(p.redactor)
	switch {
	case err != nil:
		p.logger.Printf("[pulse] failed to re-redact stored error records: %v", err)
	case n > 0:
		p.logger.Printf("[pulse] re-redacted %d error records stored by an earlier Pulse version", n)
	}
}
