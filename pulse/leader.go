package pulse

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Several instances of an application can share one storage backend
// (PostgreSQL). Each records its own requests, queries, runtime samples and
// health results, tagged with its InstanceID, and evaluates the alert rules
// about itself: memory, goroutines, health, dropped writes. Work that must
// happen once for the whole cluster runs only on the leader — evaluating
// cluster-wide alert rules and SLO burn rates, sending their notifications,
// and sweeping retention. Leadership is a PostgreSQL advisory lock owned by
// one instance's database session; when that instance stops, or its
// connection dies, the lock is released and another instance takes over at
// its next campaign.

const leaderCampaignInterval = 5 * time.Second

// leaderStaleAfter is how long the leader's note in pulse_meta stays current
// without being refreshed.
const leaderStaleAfter = 3 * leaderCampaignInterval

const leaderMetaKey = "leader"

// leaderElector is implemented by storage backends. Unexported for the same
// reason as rollupStore.
type leaderElector interface {
	// sharedStorage reports whether other instances may use the same storage.
	sharedStorage() bool
	// campaign acquires or keeps leadership for instance, and reports
	// whether instance leads.
	campaign(ctx context.Context, instance string) bool
	// resign gives leadership up, if held.
	resign()
	// leaderID returns the instance that last reported leading, if that
	// report is current.
	leaderID() string
}

// sharedStorage reports whether other instances may record into the same
// storage as this one.
func (p *Pulse) sharedStorage() bool {
	le, ok := p.storage.(leaderElector)
	return ok && le.sharedStorage()
}

// isLeader reports whether this instance runs cluster-wide work. On storage
// no other instance shares, it always does.
func (p *Pulse) isLeader() bool {
	if !p.sharedStorage() {
		return true
	}
	return p.leader.Load()
}

// startLeaderElection campaigns for leadership now and then on an interval,
// when the storage is shared.
func startLeaderElection(p *Pulse) {
	if !p.sharedStorage() {
		return
	}
	le := p.storage.(leaderElector)
	p.campaign()
	p.startBackground("leader-election", func(ctx context.Context) {
		ticker := time.NewTicker(leaderCampaignInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				le.resign()
				p.leader.Store(false)
				return
			case <-ticker.C:
				p.campaign()
			}
		}
	})
}

// campaign runs one election round and reacts to a change of role.
func (p *Pulse) campaign() {
	le, ok := p.storage.(leaderElector)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, leaderCampaignInterval)
	leading := le.campaign(ctx, p.config.InstanceID)
	cancel()
	if was := p.leader.Swap(leading); was == leading {
		return
	}
	if !leading {
		p.logger.Printf("[pulse] instance %q is no longer the leader", p.config.InstanceID)
		return
	}
	p.logger.Printf("[pulse] instance %q is now the leader: it evaluates cluster-wide alerts and SLOs "+
		"and sweeps retention for every instance", p.config.InstanceID)
	// Take over the alerts the previous leader left open, so a breach it
	// already announced isn't announced again.
	if p.alertEngine != nil {
		p.alertEngine.adoptOpenAlerts()
	}
	if p.sloEvaluator != nil {
		p.sloEvaluator.adoptOpenAlerts()
	}
}

// --- sqlStore ---

// sqlLeadership is a sqlStore's hold on leadership: the connection whose
// database session owns the advisory lock.
type sqlLeadership struct {
	mu       sync.Mutex
	conn     *sql.Conn
	instance string
}

func (s *sqlStore) sharedStorage() bool { return s.d.postgres }

func (s *sqlStore) leaderLockKey() int64 { return advisoryKey("leader:" + s.d.schema) }

func (s *sqlStore) campaign(ctx context.Context, instance string) bool {
	if !s.d.postgres {
		return true
	}
	s.lead.mu.Lock()
	defer s.lead.mu.Unlock()

	if s.lead.conn != nil {
		if err := s.lead.conn.PingContext(ctx); err == nil {
			s.noteLeader(instance)
			return true
		}
		// The session, and the lock with it, is gone.
		discardConn(s.lead.conn)
		s.lead.conn = nil
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false
	}
	var acquired bool
	err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, s.leaderLockKey()).Scan(&acquired)
	switch {
	case err != nil:
		// It may hold the lock without our knowing: don't return it to the pool.
		discardConn(conn)
		return false
	case !acquired:
		_ = conn.Close()
		return false
	}
	s.lead.conn, s.lead.instance = conn, instance
	s.noteLeader(instance)
	return true
}

func (s *sqlStore) resign() {
	s.lead.mu.Lock()
	defer s.lead.mu.Unlock()
	if s.lead.conn == nil {
		return
	}
	// Unlocking lets the next leader take over at once; discarding the
	// connection ends the session, which would release the lock anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = s.lead.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, s.leaderLockKey())
	cancel()
	discardConn(s.lead.conn)
	s.lead.conn = nil
	_, _ = s.exec(`DELETE FROM pulse_meta WHERE key = ? AND value LIKE ?`,
		leaderMetaKey, likeEscaper.Replace(s.lead.instance)+"|%")
}

// noteLeader records which instance leads, for the dashboard.
func (s *sqlStore) noteLeader(instance string) {
	_, _ = s.exec(`INSERT INTO pulse_meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		leaderMetaKey, instance+"|"+strconv.FormatInt(time.Now().UnixMilli(), 10))
}

func (s *sqlStore) leaderID() string {
	if !s.d.postgres {
		return ""
	}
	var v string
	if err := s.queryRow(`SELECT value FROM pulse_meta WHERE key = ?`, leaderMetaKey).Scan(&v); err != nil {
		return ""
	}
	i := strings.LastIndexByte(v, '|')
	if i < 0 {
		return ""
	}
	at, err := strconv.ParseInt(v[i+1:], 10, 64)
	if err != nil || time.Since(time.UnixMilli(at)) > leaderStaleAfter {
		return ""
	}
	return v[:i]
}

// discardConn closes c's database connection instead of returning it to the
// pool, ending its session and any advisory lock the session holds.
func discardConn(c *sql.Conn) {
	_ = c.Raw(func(any) error { return driver.ErrBadConn })
	_ = c.Close()
}

var _ leaderElector = (*sqlStore)(nil)

// --- Instances ---

// instanceInfo describes one instance recording into this storage.
type instanceInfo struct {
	ID        string     `json:"id"`
	Self      bool       `json:"self"`
	Leader    bool       `json:"leader"`
	Status    string     `json:"status"` // "running", "stopped" or "unresponsive"
	StartedAt *time.Time `json:"started_at,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	// Last hour, from the rollups.
	Requests  int64   `json:"requests"`
	ErrorRate float64 `json:"error_rate"`
	RPM       float64 `json:"rpm"`
}

// instanceAliveWithin is how recently an instance must have stored a runtime
// sample to count as running.
const instanceAliveWithin = time.Minute

// instancesHandler serves GET /pulse/api/instances: every instance that
// recorded into this storage in the last day, this one first.
func instancesHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := p.now()
		byID := map[string]*instanceInfo{}
		get := func(id string) *instanceInfo {
			if byID[id] == nil {
				byID[id] = &instanceInfo{ID: id, Status: "unresponsive"}
			}
			return byID[id]
		}
		self := get(p.config.InstanceID)
		self.Self, self.Status = true, "running"
		started := p.startTime
		self.StartedAt, self.LastSeen = &started, &now

		stopped := map[string]bool{}
		if ls, ok := p.storage.(lifecycleStore); ok {
			events, _ := ls.lifecycleEvents(TimeRange{Start: now.Add(-24 * time.Hour), End: now.Add(time.Minute)})
			for _, e := range events { // oldest first
				if e.InstanceID == "" || e.InstanceID == p.config.InstanceID {
					continue
				}
				in, at := get(e.InstanceID), e.At
				if e.Type == lifecycleStart {
					in.StartedAt = &at
					stopped[e.InstanceID] = false
				} else {
					stopped[e.InstanceID] = true
				}
			}
		}
		// Every instance stores a runtime sample every few seconds.
		samples, _ := p.storage.GetRuntimeHistory(TimeRange{Start: now.Add(-2 * instanceAliveWithin), End: now.Add(time.Minute)})
		for _, s := range samples {
			if s.InstanceID == "" || s.InstanceID == p.config.InstanceID {
				continue
			}
			in, at := get(s.InstanceID), s.Timestamp
			if in.LastSeen == nil || at.After(*in.LastSeen) {
				in.LastSeen = &at
			}
		}
		for id, in := range byID {
			switch {
			case in.Self:
			case in.LastSeen != nil && now.Sub(*in.LastSeen) < instanceAliveWithin:
				in.Status = "running"
			case stopped[id]:
				in.Status = "stopped"
			}
		}

		for id, counts := range p.rollups.instanceSummaries(p.config.InstanceID, now.Add(-time.Hour), now) {
			in := get(id)
			in.Requests, _, in.ErrorRate, in.RPM = rollupRates(counts, 60)
		}

		leader := ""
		if p.isLeader() {
			leader = p.config.InstanceID
		} else if le, ok := p.storage.(leaderElector); ok {
			leader = le.leaderID()
		}
		list := make([]*instanceInfo, 0, len(byID))
		for _, in := range byID {
			in.Leader = in.ID == leader
			list = append(list, in)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Self != list[j].Self {
				return list[i].Self
			}
			return list[i].ID < list[j].ID
		})
		c.JSON(http.StatusOK, gin.H{"instances": list, "shared": p.sharedStorage(), "leader": leader, "self": p.config.InstanceID})
	}
}

// matchesInstance reports whether a record from instance rec passes a
// filter for want: every record passes an empty filter, and records stored
// before v1.2, which have no instance, count as this instance's.
func (p *Pulse) matchesInstance(rec, want string) bool {
	return want == "" || rec == want || (rec == "" && want == p.config.InstanceID)
}

// instanceParam is the ?instance= filter, defaulting to this instance.
func (p *Pulse) instanceParam(c *gin.Context) string {
	if v := c.Query("instance"); v != "" {
		return v
	}
	return p.config.InstanceID
}
