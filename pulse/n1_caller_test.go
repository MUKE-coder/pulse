package pulse

import (
	"strings"
	"testing"
	"time"
)

// The detector already knows which line issued the repeated query — the
// whole point of an N+1 finding is to take you there.
func TestN1_CarriesTheCallerThatIssuedTheQuery(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	traceID := GenerateTraceID()
	ctx := ContextWithTraceID(db.Statement.Context, traceID)
	for i := 0; i < 8; i++ {
		var users []TestUser
		db.WithContext(ctx).Where("age > ?", i).Find(&users)
	}
	p.gormPlugin.finalizeTrace(traceID, "GET /api/orders")

	found, err := p.storage.GetN1Detections(wideRange())
	if err != nil || len(found) == 0 {
		t.Fatalf("no detection recorded: %v %+v", err, found)
	}
	d := found[0]
	if d.CallerFile == "" || d.CallerLine == 0 {
		t.Fatalf("detection should name the call site: %+v", d)
	}
	// findCaller skips frames inside Pulse itself, and this test runs from
	// inside package pulse, so the first frame outside is testing.go. In an
	// application it is the file that ran the loop; what matters here is
	// that a real file and line travel with the detection.
	if !strings.HasSuffix(d.CallerFile, ".go") {
		t.Errorf("caller %q does not look like a source file", d.CallerFile)
	}

	// And it survives the ranking, which is what the table reads.
	ranked := rankN1Detections(found, 10)
	if len(ranked) == 0 {
		t.Fatal("nothing ranked")
	}
	if ranked[0].CallerFile != d.CallerFile || ranked[0].CallerLine != d.CallerLine {
		t.Errorf("ranking lost the caller: %+v", ranked[0])
	}
	if ranked[0].Route != "GET /api/orders" || ranked[0].SuggestedFix == "" {
		t.Errorf("ranking should keep the route and the suggestion: %+v", ranked[0])
	}
}

// Every backend must store and return it, or the Database page shows a
// dash on SQLite and a path on Memory.
func TestConformance_N1DetectionsKeepTheCaller(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		detection := N1Detection{
			Pattern: "SELECT * FROM items WHERE order_id = ?", Count: 24,
			TotalDuration: 120 * time.Millisecond, AvgDuration: 5 * time.Millisecond,
			RequestTraceID: "trace-1", Route: "GET /api/orders",
			SuggestedFix: "use Preload", CallerFile: "internal/services/catalog_report.go",
			CallerLine: 148, DetectedAt: time.Now().Add(-time.Minute),
		}
		if err := s.StoreN1Detection(detection); err != nil {
			t.Fatalf("store: %v", err)
		}
		got, err := s.GetN1Detections(wideRange())
		if err != nil || len(got) != 1 {
			t.Fatalf("read back %d detections: %v", len(got), err)
		}
		if got[0].CallerFile != detection.CallerFile || got[0].CallerLine != detection.CallerLine {
			t.Errorf("caller came back as %q:%d", got[0].CallerFile, got[0].CallerLine)
		}
	})
}

// The trace waterfall should be self-sufficient: each query span says which
// line ran it.
func TestTraceSpans_QueriesCarryTheCaller(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	traceID := GenerateTraceID()
	ctx := ContextWithTraceID(db.Statement.Context, traceID)
	var users []TestUser
	db.WithContext(ctx).Where("age > ?", 1).Find(&users)

	store, ok := p.storage.(traceStore)
	if !ok {
		t.Fatal("memory storage should serve trace spans")
	}
	spans, err := store.traceSpans(traceID)
	if err != nil {
		t.Fatalf("spans: %v", err)
	}
	var query *traceSpan
	for i := range spans {
		if spans[i].Kind == "query" {
			query = &spans[i]
		}
	}
	if query == nil {
		t.Fatalf("no query span among %d", len(spans))
	}
	if !strings.Contains(query.Caller, ".go:") {
		t.Errorf("query span caller is %q, want a file:line", query.Caller)
	}
}
