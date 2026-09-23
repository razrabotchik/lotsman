package audit

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sampleEvent() Event {
	return Event{
		SchemaVersion: SchemaVersion,
		Time:          time.Unix(0, 0).UTC(),
		Operation:     "default:GET:/pets/{petId}",
		Tool:          "get_pet",
		Effect:        "read",
		Decision:      DecisionExecuted,
		Origin:        "https://api.example.com",
		Method:        "GET",
		Path:          "/pets/{petId}",
		Status:        200,
		DurationMs:    12,
		ResponseBytes: 34,
	}
}

// The event goes through the process logger, which is where it picks up
// redaction and the stdout ban. This asserts the fields arrive, so that a
// later refactor cannot quietly stop recording one.
func TestLogSinkWritesTheEvent(t *testing.T) {
	var out bytes.Buffer
	Log(slog.New(slog.NewTextHandler(&out, nil))).Record(sampleEvent())

	line := out.String()
	for _, want := range []string{
		"msg=audit",
		"schemaVersion=1",
		`operation=default:GET:/pets/{petId}`,
		"tool=get_pet",
		"effect=read",
		"decision=executed",
		"status=200",
		"durationMs=12",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the record does not carry %q:\n%s", want, line)
		}
	}
}

func TestNilLoggerRecordsNothingRatherThanPanicking(t *testing.T) {
	Log(nil).Record(sampleEvent())
	Discard{}.Record(sampleEvent())
	Multi{nil, Discard{}}.Record(sampleEvent())
}

func TestMultiFansOut(t *testing.T) {
	first, second := NewCounters(), NewCounters()
	Multi{first, second}.Record(sampleEvent())
	for i, counters := range []*Counters{first, second} {
		if counters.decisions[DecisionExecuted] != 1 {
			t.Errorf("sink %d did not receive the event", i)
		}
	}
}

// The scrape is the aggregate an operator watches (U13), rendered without a
// client library.
func TestMetricsExposition(t *testing.T) {
	counters := NewCounters()
	counters.Record(sampleEvent())

	refused := sampleEvent()
	refused.Decision = DecisionRefused
	refused.Reason = "policy"
	refused.Status = 0
	counters.Record(refused)

	failing := sampleEvent()
	failing.Status = 503
	counters.Record(failing)

	rec := httptest.NewRecorder()
	counters.Handler(func() Snapshot { return Snapshot{Tools: 7, Digest: "sha256:abc"} }).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))

	body := rec.Body.String()
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q", got)
	}
	for _, want := range []string{
		"# TYPE lotsman_tool_calls_total counter",
		`lotsman_tool_calls_total{decision="executed"} 2`,
		`lotsman_tool_calls_total{decision="refused"} 1`,
		`lotsman_refusals_total{reason="policy"} 1`,
		`lotsman_upstream_responses_total{class="2xx"} 1`,
		`lotsman_upstream_responses_total{class="5xx"} 1`,
		"lotsman_catalog_tools 7",
		`lotsman_catalog_info{digest="sha256:abc"} 1`,
		"lotsman_call_duration_seconds_count 3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not contain %q:\n%s", want, body)
		}
	}
}

// Two scrapes that saw the same events are byte-identical. Map order would
// make a diff of two scrapes unreadable, and determinism is cheap here
// (Principle IV).
func TestScrapesAreDeterministic(t *testing.T) {
	counters := NewCounters()
	for _, reason := range []string{"policy", "usage", "auth", "upstream"} {
		event := sampleEvent()
		event.Decision = DecisionRefused
		event.Reason = reason
		counters.Record(event)
	}

	scrape := func() string {
		rec := httptest.NewRecorder()
		counters.Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
		return rec.Body.String()
	}
	if first, second := scrape(), scrape(); first != second {
		t.Errorf("two scrapes of the same counters differ:\n%s\n---\n%s", first, second)
	}
}

// With no catalog to describe, the gauges are absent rather than zero: "no
// catalog" and "a catalog with no tools" are different, and a dashboard
// cannot tell them apart after the fact.
func TestAbsentSnapshotOmitsTheCatalogGauges(t *testing.T) {
	rec := httptest.NewRecorder()
	NewCounters().Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if strings.Contains(rec.Body.String(), "lotsman_catalog_tools") {
		t.Error("the catalog gauge was reported without a catalog")
	}
}

// A label value that contains the exposition format's own syntax must not be
// able to forge a sample line.
func TestLabelValuesAreEscaped(t *testing.T) {
	rec := httptest.NewRecorder()
	NewCounters().Handler(func() Snapshot { return Snapshot{Digest: "a\"b\\c\nd"} }).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))

	body := rec.Body.String()
	if !strings.Contains(body, `lotsman_catalog_info{digest="a\"b\\c\nd"} 1`) {
		t.Errorf("the label value was not escaped:\n%s", body)
	}
	// One line, not two: an embedded newline that survived would be a forged
	// sample.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "d\"") {
			t.Errorf("a label value broke out onto its own line:\n%s", body)
		}
	}
}
