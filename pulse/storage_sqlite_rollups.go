package pulse

import (
	"sort"
	"time"
)

// Rollup persistence for SQLiteStorage; see rollup.go. The tables are part
// of sqliteSchema.

var _ rollupStore = (*SQLiteStorage)(nil)

// saveRollups upserts the given minutes in one transaction. Minutes are
// saved whole, so saving the same minute again simply replaces it.
func (s *SQLiteStorage) saveRollups(minutes []minuteSnapshot) error {
	if len(minutes) == 0 {
		return nil
	}
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed

	routeStmt, err := tx.Prepare(`INSERT OR REPLACE INTO request_rollups
		(minute, method, route, total, status_4xx, status_5xx, latency_ns) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	histStmt, err := tx.Prepare(`INSERT OR REPLACE INTO latency_rollups (minute, hist) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	sloStmt, err := tx.Prepare(`INSERT OR REPLACE INTO slo_rollups (minute, slo, good, total) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}

	for _, m := range minutes {
		for k, c := range m.routes {
			if _, err := routeStmt.Exec(m.minute, k.method, k.route,
				c.total, c.status4xx, c.status5xx, int64(c.latency)); err != nil {
				return err
			}
		}
		if m.hist != nil {
			if _, err := histStmt.Exec(m.minute, m.hist.encode()); err != nil {
				return err
			}
		}
		for name, c := range m.slos {
			if _, err := sloStmt.Exec(m.minute, name, c.good, c.total); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// loadRollups returns every persisted minute at or after sinceMinute.
func (s *SQLiteStorage) loadRollups(sinceMinute int64) ([]minuteSnapshot, error) {
	s.sync()
	byMinute := make(map[int64]*minuteSnapshot)
	get := func(m int64) *minuteSnapshot {
		snap := byMinute[m]
		if snap == nil {
			snap = &minuteSnapshot{minute: m, slos: make(map[string]sloCount)}
			byMinute[m] = snap
		}
		return snap
	}

	rows, err := s.db.Query(`SELECT minute, method, route, total, status_4xx, status_5xx, latency_ns
		FROM request_rollups WHERE minute >= ?`, sinceMinute)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m       int64
			k       rollupRouteKey
			c       routeCounts
			latency int64
		)
		if err := rows.Scan(&m, &k.method, &k.route, &c.total, &c.status4xx, &c.status5xx, &latency); err != nil {
			rows.Close()
			return nil, err
		}
		c.latency = time.Duration(latency)
		snap := get(m)
		if snap.routes == nil {
			snap.routes = make(map[rollupRouteKey]routeCounts)
		}
		snap.routes[k] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(`SELECT minute, hist FROM latency_rollups WHERE minute >= ?`, sinceMinute)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m    int64
			blob []byte
		)
		if err := rows.Scan(&m, &blob); err != nil {
			rows.Close()
			return nil, err
		}
		h := decodeLatencyHistogram(blob)
		get(m).hist = &h
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(`SELECT minute, slo, good, total FROM slo_rollups WHERE minute >= ?`, sinceMinute)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m    int64
			name string
			c    sloCount
		)
		if err := rows.Scan(&m, &name, &c.good, &c.total); err != nil {
			rows.Close()
			return nil, err
		}
		get(m).slos[name] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]minuteSnapshot, 0, len(byMinute))
	for _, snap := range byMinute {
		out = append(out, *snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].minute < out[j].minute })
	return out, nil
}

// pruneRollups deletes per-route and latency rollups before detailBefore and
// SLO rollups before sloBefore (Unix minutes).
func (s *SQLiteStorage) pruneRollups(detailBefore, sloBefore int64) error {
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM request_rollups WHERE minute < ?`, detailBefore); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM latency_rollups WHERE minute < ?`, detailBefore); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM slo_rollups WHERE minute < ?`, sloBefore)
	return err
}
