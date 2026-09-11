package pulse

import (
	"sort"
	"time"
)

// Rollup persistence for the SQL backends; see rollup.go. The tables are part
// of sqliteSchema. Each instance saves its own rows, and loads the other
// instances' as they save them. Rows saved before v1.2 have no instance and
// count as the loading instance's own.

var _ rollupStore = (*SQLiteStorage)(nil)

// saveRollups upserts the given minutes of instance in one transaction.
// Minutes are saved whole, so saving the same minute again replaces it.
func (s *sqlStore) saveRollups(instance string, minutes []minuteSnapshot) error {
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

	routeStmt, err := tx.Prepare(s.d.sql(`INSERT INTO request_rollups
		(instance_id, minute, method, route, total, status_4xx, status_5xx, latency_ns, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, minute, method, route) DO UPDATE SET total = excluded.total,
		    status_4xx = excluded.status_4xx, status_5xx = excluded.status_5xx,
		    latency_ns = excluded.latency_ns, updated_at = excluded.updated_at`))
	if err != nil {
		return err
	}
	histStmt, err := tx.Prepare(s.d.sql(`INSERT INTO latency_rollups (instance_id, minute, hist, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (instance_id, minute) DO UPDATE SET hist = excluded.hist, updated_at = excluded.updated_at`))
	if err != nil {
		return err
	}
	sloStmt, err := tx.Prepare(s.d.sql(`INSERT INTO slo_rollups (instance_id, minute, slo, good, total, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, minute, slo) DO UPDATE SET good = excluded.good, total = excluded.total,
		    updated_at = excluded.updated_at`))
	if err != nil {
		return err
	}

	updated := time.Now().UnixMilli()
	for _, m := range minutes {
		for k, c := range m.routes {
			if _, err := routeStmt.Exec(s.d.args([]any{instance, m.minute, k.method, k.route,
				c.total, c.status4xx, c.status5xx, int64(c.latency), updated})...); err != nil {
				return err
			}
		}
		if m.hist != nil {
			if _, err := histStmt.Exec(s.d.args([]any{instance, m.minute, m.hist.encode(), updated})...); err != nil {
				return err
			}
		}
		for name, c := range m.slos {
			if _, err := sloStmt.Exec(s.d.args([]any{instance, m.minute, name, c.good, c.total, updated})...); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// loadRollups returns instance's persisted minutes at or after sinceMinute,
// including those saved before v1.2 without an instance.
func (s *sqlStore) loadRollups(instance string, sinceMinute int64) ([]minuteSnapshot, error) {
	own := func(string) string { return instance }
	byInstance, _, err := s.loadRollupRows(own, `instance_id IN (?, '') AND minute >= ?`, instance, sinceMinute)
	return byInstance[instance], err
}

// loadPeerRollups returns the minutes at or after sinceMinute that instances
// other than instance saved after updatedAfter (Unix milliseconds), by
// instance, and the latest save time among them.
func (s *sqlStore) loadPeerRollups(instance string, sinceMinute, updatedAfter int64) (map[string][]minuteSnapshot, int64, error) {
	peer := func(id string) string { return id }
	return s.loadRollupRows(peer, `instance_id NOT IN (?, '') AND minute >= ? AND updated_at > ?`,
		instance, sinceMinute, updatedAfter)
}

// loadRollupRows reads the rollup rows matching where from all three
// tables, groups them into minutes under keyOf(instance_id), and returns the
// latest updated_at seen.
func (s *sqlStore) loadRollupRows(keyOf func(string) string, where string, args ...any) (map[string][]minuteSnapshot, int64, error) {
	s.sync()
	type key struct {
		instance string
		minute   int64
	}
	snaps := make(map[key]*minuteSnapshot)
	get := func(instance string, m int64) *minuteSnapshot {
		k := key{keyOf(instance), m}
		snap := snaps[k]
		if snap == nil {
			snap = &minuteSnapshot{minute: m, slos: make(map[string]sloCount)}
			snaps[k] = snap
		}
		return snap
	}
	var latest int64
	seen := func(updated int64) {
		if updated > latest {
			latest = updated
		}
	}

	rows, err := s.query(`SELECT instance_id, minute, method, route, total, status_4xx, status_5xx, latency_ns, updated_at
		FROM request_rollups WHERE `+where, args...)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var (
			instance        string
			m, lat, updated int64
			k               rollupRouteKey
			c               routeCounts
		)
		if err := rows.Scan(&instance, &m, &k.method, &k.route, &c.total, &c.status4xx, &c.status5xx, &lat, &updated); err != nil {
			rows.Close()
			return nil, 0, err
		}
		c.latency = time.Duration(lat)
		snap := get(instance, m)
		if snap.routes == nil {
			snap.routes = make(map[rollupRouteKey]routeCounts)
		}
		snap.routes[k] = c
		seen(updated)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	rows, err = s.query(`SELECT instance_id, minute, hist, updated_at FROM latency_rollups WHERE `+where, args...)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var (
			instance   string
			m, updated int64
			blob       []byte
		)
		if err := rows.Scan(&instance, &m, &blob, &updated); err != nil {
			rows.Close()
			return nil, 0, err
		}
		h := decodeLatencyHistogram(blob)
		get(instance, m).hist = &h
		seen(updated)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	rows, err = s.query(`SELECT instance_id, minute, slo, good, total, updated_at FROM slo_rollups WHERE `+where, args...)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var (
			instance, name string
			m, updated     int64
			c              sloCount
		)
		if err := rows.Scan(&instance, &m, &name, &c.good, &c.total, &updated); err != nil {
			rows.Close()
			return nil, 0, err
		}
		get(instance, m).slos[name] = c
		seen(updated)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	out := make(map[string][]minuteSnapshot)
	for k, snap := range snaps {
		out[k.instance] = append(out[k.instance], *snap)
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i].minute < list[j].minute })
	}
	return out, latest, nil
}

// pruneRollups deletes per-route and latency rollups before detailBefore and
// SLO rollups before sloBefore (Unix minutes), for every instance.
func (s *sqlStore) pruneRollups(detailBefore, sloBefore int64) error {
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.exec(`DELETE FROM request_rollups WHERE minute < ?`, detailBefore); err != nil {
		return err
	}
	if _, err := s.exec(`DELETE FROM latency_rollups WHERE minute < ?`, detailBefore); err != nil {
		return err
	}
	_, err := s.exec(`DELETE FROM slo_rollups WHERE minute < ?`, sloBefore)
	return err
}
