package pulse

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// suggestN1Fix returns a one-sentence hint pointing the developer at the
// likely fix for an N+1 produced by the given normalised SQL. Returns "" when
// no confident match — better to stay silent than emit guesses that don't
// apply.
//
// The matchers are deliberately conservative. They look for the structural
// shape of "one parameterised lookup repeated per row" rather than full SQL
// parsing — fast, allocation-free, and good enough for GORM's output.
func suggestN1Fix(normalizedSQL string) string {
	sql := strings.ToLower(strings.TrimSpace(normalizedSQL))
	if sql == "" {
		return ""
	}

	// Order matters: the more specific patterns (count, exists) need to be
	// checked before the generic foreign-key matcher, otherwise
	// "select count(*) from posts where author_id = ?" misclassifies as a
	// foreign-key lookup.
	switch {
	case n1Count.MatchString(sql):
		// Aggregate-per-row — almost always solvable by GROUP BY in the outer query.
		return "Aggregate in a single query — `SELECT parent_id, COUNT(*) … GROUP BY parent_id` — " +
			"and join against the parents instead of running one COUNT per parent."

	case n1Exists.MatchString(sql):
		return "Replace the per-row existence check with a join, or precompute a set membership " +
			"in a single query and look up in-memory."

	case n1ByPrimaryKey.MatchString(sql):
		// "SELECT … FROM foo WHERE id = ?" — classic single-row-by-pk in a loop.
		return "Eager-load the related rows. GORM: `db.Preload(\"<Relation>\").Find(...)` " +
			"or `db.Joins(\"<Relation>\").Find(...)`."

	case n1ByForeignKey.MatchString(sql):
		// "SELECT … FROM foo WHERE x_id = ?" — children fetched per parent.
		return "Batch-load the children. GORM: `db.Preload(\"<Children>\").Find(&parents)`. " +
			"Or build an `IN (?)` query with the parent IDs collected from the outer loop."

	case n1LimitOne.MatchString(sql):
		// "SELECT … LIMIT 1" repeated — same shape as PK lookup, often a First() call.
		return "This looks like `.First(...)` in a loop. Use `.Preload(...)` on the outer " +
			"query so the related row is fetched alongside its parent."
	}

	// Fall through: still useful to surface a generic nudge.
	return "Look for a `for { db.… }` loop near this handler. Most N+1s collapse into " +
		"one query with `Preload`, `Joins`, or an `IN (?)` clause."
}

var (
	// "select … from <table> where … id = ?"  (also handles backticks/quotes)
	n1ByPrimaryKey = regexp.MustCompile(`(?s)^select\s.+\sfrom\s+[\w` + "`" + `".]+\s+where\s+.*\bid\b\s*=\s*\?`)

	// "where <something>_id = ?" — foreign-key lookup.
	n1ByForeignKey = regexp.MustCompile(`(?s)^select\s.+\sfrom\s+[\w` + "`" + `".]+\s+where\s+.*\b\w+_id\b\s*=\s*\?`)

	// "limit 1" anywhere after a SELECT — often paired with .First()
	n1LimitOne = regexp.MustCompile(`(?s)^select\s.+\slimit\s+1\b`)

	// "select count(...)" repeated — aggregate-per-row symptom.
	n1Count = regexp.MustCompile(`^select\s+count\s*\(`)

	// "select 1 from ..." or "select exists ..." — per-row existence check.
	n1Exists = regexp.MustCompile(`^select\s+(1|exists)\b`)
)

// rankN1Detections groups raw N+1 detections by (route, normalised SQL) and
// returns them ranked by impact score (occurrences × avg queries/hit × avg
// query duration). This matches issue #2's prioritisation: "highest-value
// optimisation per hour spent."
//
// `route` is treated case-insensitively for the grouping key (same handler
// hit via different casing of the path is still the same handler), but the
// returned Route preserves the most recent observed casing for display.
func rankN1Detections(in []N1Detection, limit int) []N1Ranking {
	if len(in) == 0 {
		return nil
	}

	type aggKey struct{ route, pattern string }
	type aggVal struct {
		displayRoute string
		count        int           // number of occurrences (distinct trace IDs)
		totalQueries int           // sum of Count across occurrences
		totalDur     time.Duration // sum of TotalDuration across occurrences
		first        time.Time
		last         time.Time
		fix          string
	}
	groups := make(map[aggKey]*aggVal)

	for _, d := range in {
		k := aggKey{strings.ToLower(d.Route), d.Pattern}
		v, ok := groups[k]
		if !ok {
			v = &aggVal{
				displayRoute: d.Route,
				first:        d.DetectedAt,
				last:         d.DetectedAt,
				fix:          d.SuggestedFix,
			}
			groups[k] = v
		}
		v.count++
		v.totalQueries += d.Count
		v.totalDur += d.TotalDuration
		if d.DetectedAt.Before(v.first) {
			v.first = d.DetectedAt
		}
		if d.DetectedAt.After(v.last) {
			v.last = d.DetectedAt
			v.displayRoute = d.Route // keep most recent display casing
		}
		// Prefer a non-empty suggestion if a later detection produced one.
		if v.fix == "" && d.SuggestedFix != "" {
			v.fix = d.SuggestedFix
		}
	}

	out := make([]N1Ranking, 0, len(groups))
	for k, v := range groups {
		avgQ := float64(v.totalQueries) / float64(v.count)
		var avgDur time.Duration
		if v.totalQueries > 0 {
			avgDur = v.totalDur / time.Duration(v.totalQueries)
		}
		out = append(out, N1Ranking{
			Route:            v.displayRoute,
			Pattern:          k.pattern,
			Occurrences:      v.count,
			AvgQueriesPerHit: avgQ,
			AvgQueryDuration: avgDur,
			TotalDuration:    v.totalDur,
			// Impact: total wall-clock cost, in milliseconds for readability.
			ImpactScore:  float64(v.totalDur) / float64(time.Millisecond),
			SuggestedFix: v.fix,
			FirstSeen:    v.first,
			LastSeen:     v.last,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ImpactScore > out[j].ImpactScore })

	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}
