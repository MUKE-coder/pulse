package pulse

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Lifecycle events mark each Pulse process starting and stopping, so the
// dashboard can tell a gap in the timelines (Pulse wasn't running, or lost
// in-memory data) from a quiet period. With the SQLite backend, a start
// whose previous run never recorded a stop is flagged as following an
// unclean shutdown — a crash, OOM kill or kill -9 — and carries the time of
// the last data recorded before it.

const (
	lifecycleStart = "start"
	lifecycleStop  = "stop"
)

// lifecycleEvent is one process start or stop.
type lifecycleEvent struct {
	Type       string    `json:"type"` // "start" | "stop"
	InstanceID string    `json:"instance_id"`
	At         time.Time `json:"at"`

	// PreviousUnclean is set on a start whose previous run ended without a
	// stop event.
	PreviousUnclean bool `json:"previous_unclean,omitempty"`
	// GapFrom is, for an unclean start, the last request recorded before it.
	GapFrom *time.Time `json:"gap_from,omitempty"`
}

// lifecycleStore is implemented by storage backends that keep lifecycle
// events. Unexported for the same reason as rollupStore.
type lifecycleStore interface {
	storeLifecycleEvent(e lifecycleEvent) error
	lifecycleEvents(tr TimeRange) ([]lifecycleEvent, error)
	lastLifecycleEvent(instanceID string) (*lifecycleEvent, error)
	lastRequestAt() (time.Time, bool)
}

// recordStart stores this process's start event, flagging it when the
// previous run of the same instance never recorded a stop.
func recordStart(p *Pulse) {
	ls, ok := p.storage.(lifecycleStore)
	if !ok {
		return
	}
	e := lifecycleEvent{Type: lifecycleStart, InstanceID: p.config.InstanceID, At: p.startTime}
	if prev, err := ls.lastLifecycleEvent(p.config.InstanceID); err == nil && prev != nil && prev.Type == lifecycleStart {
		e.PreviousUnclean = true
		if t, ok := ls.lastRequestAt(); ok {
			e.GapFrom = &t
		}
		p.logger.Printf("[pulse] the previous run of instance %q ended without a clean shutdown "+
			"(crash or kill); data from its last moments may be missing", p.config.InstanceID)
	}
	p.internalError("storage: lifecycle", ls.storeLifecycleEvent(e))
}

// startLifecycleWatcher records a stop event when the Pulse context ends —
// on Shutdown, or when the caller cancels the context passed to Mount.
func startLifecycleWatcher(p *Pulse) {
	ls, ok := p.storage.(lifecycleStore)
	if !ok {
		return
	}
	p.startBackground("lifecycle", func(ctx context.Context) {
		<-ctx.Done()
		e := lifecycleEvent{Type: lifecycleStop, InstanceID: p.config.InstanceID, At: time.Now()}
		p.internalError("storage: lifecycle", ls.storeLifecycleEvent(e))
	})
}

// lifecycleHandler serves GET /pulse/api/lifecycle: the start/stop events in
// the requested range, and how far back the dashboard's data reaches.
func lifecycleHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		events := []lifecycleEvent{}
		if ls, ok := p.storage.(lifecycleStore); ok {
			if got, err := ls.lifecycleEvents(parseTimeRangeParam(c)); err == nil && got != nil {
				events = got
			}
		}
		_, persistent := p.storage.(rollupStore)
		c.JSON(http.StatusOK, gin.H{
			"events":      events,
			"instance_id": p.config.InstanceID,
			"storage":     storageDriverName(p.config.Storage.Driver),
			"persistent":  persistent,
			"data_since":  p.rollups.dataSince(),
		})
	}
}

// --- MemoryStorage ---

func (s *MemoryStorage) storeLifecycleEvent(e lifecycleEvent) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.lifecycle = append(s.lifecycle, e)
	return nil
}

func (s *MemoryStorage) lifecycleEvents(tr TimeRange) ([]lifecycleEvent, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	var out []lifecycleEvent
	for _, e := range s.lifecycle {
		if (tr.Start.IsZero() || !e.At.Before(tr.Start)) && (tr.End.IsZero() || !e.At.After(tr.End)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *MemoryStorage) lastLifecycleEvent(instanceID string) (*lifecycleEvent, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	for i := len(s.lifecycle) - 1; i >= 0; i-- {
		if s.lifecycle[i].InstanceID == instanceID {
			e := s.lifecycle[i]
			return &e, nil
		}
	}
	return nil, nil
}

func (s *MemoryStorage) lastRequestAt() (time.Time, bool) {
	last := s.requests.GetLast(1)
	if len(last) == 0 {
		return time.Time{}, false
	}
	return last[0].Timestamp, true
}

// --- SQLiteStorage ---

func (s *SQLiteStorage) storeLifecycleEvent(e lifecycleEvent) error {
	var gapFrom any
	if e.GapFrom != nil {
		gapFrom = e.GapFrom.UnixNano()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO lifecycle_events (at, type, instance_id, previous_unclean, gap_from) VALUES (?, ?, ?, ?, ?)`,
		e.At.UnixNano(), e.Type, e.InstanceID, boolToInt(e.PreviousUnclean), gapFrom,
	)
	return err
}

func (s *SQLiteStorage) lifecycleEvents(tr TimeRange) ([]lifecycleEvent, error) {
	start, end := int64(0), time.Now().Add(time.Hour).UnixNano()
	if !tr.Start.IsZero() {
		start = tr.Start.UnixNano()
	}
	if !tr.End.IsZero() {
		end = tr.End.UnixNano()
	}
	rows, err := s.db.Query(
		`SELECT at, type, instance_id, previous_unclean, gap_from FROM lifecycle_events
		 WHERE at BETWEEN ? AND ? ORDER BY at ASC`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lifecycleEvent
	for rows.Next() {
		e, err := scanLifecycleEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) lastLifecycleEvent(instanceID string) (*lifecycleEvent, error) {
	row := s.db.QueryRow(
		`SELECT at, type, instance_id, previous_unclean, gap_from FROM lifecycle_events
		 WHERE instance_id = ? ORDER BY at DESC LIMIT 1`, instanceID)
	e, err := scanLifecycleEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *SQLiteStorage) lastRequestAt() (time.Time, bool) {
	s.sync()
	var ts sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(timestamp) FROM requests`).Scan(&ts); err != nil || !ts.Valid {
		return time.Time{}, false
	}
	return time.Unix(0, ts.Int64), true
}

func scanLifecycleEvent(r rowScanner) (lifecycleEvent, error) {
	var (
		e       lifecycleEvent
		at      int64
		unclean int
		gapFrom sql.NullInt64
	)
	if err := r.Scan(&at, &e.Type, &e.InstanceID, &unclean, &gapFrom); err != nil {
		return e, err
	}
	e.At = time.Unix(0, at)
	e.PreviousUnclean = unclean != 0
	if gapFrom.Valid {
		t := time.Unix(0, gapFrom.Int64)
		e.GapFrom = &t
	}
	return e, nil
}

var (
	_ lifecycleStore = (*MemoryStorage)(nil)
	_ lifecycleStore = (*SQLiteStorage)(nil)
)
