package audit

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
)

// Counters is the aggregate an operator watches (U13), tallied from the same
// events the audit log records.
//
// The numbers come from events rather than from instrumentation sprinkled
// through the call path, because a metric and a log line that disagree about
// how many calls were refused are worse than either alone.
//
// There is no client library. The exposition format is a few lines of text
// and a dependency would buy conventions this needs none of (Constitution
// VII); U13 asks for aggregated statistics, not for a particular library.
type Counters struct {
	mu sync.Mutex

	decisions map[Decision]uint64
	refusals  map[string]uint64
	statuses  map[string]uint64

	durationSeconds float64
	durationCount   uint64
	// durationBuckets counts calls at or under each of durationBounds, not
	// cumulatively; the exposition adds them up.
	durationBuckets []uint64
}

// durationBounds are the histogram's upper bounds, in seconds: from a cached
// read to a call that spent the whole default timeout and then some. A sum
// and a count give an average, and an average hides exactly the slow calls an
// operator is looking for.
var durationBounds = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// NewCounters returns an empty tally.
func NewCounters() *Counters {
	return &Counters{
		decisions: map[Decision]uint64{},
		refusals:  map[string]uint64{},
		statuses:  map[string]uint64{},

		durationBuckets: make([]uint64, len(durationBounds)),
	}
}

// Record tallies one event.
//
//nolint:gocritic // hugeParam: an Event travels by value so that a sink cannot edit the record the next sink in a Multi is about to write.
func (c *Counters) Record(event Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.decisions[event.Decision]++
	if event.Decision == DecisionRefused && event.Reason != "" {
		c.refusals[event.Reason]++
	}
	if event.Status > 0 {
		// By class, not by code: an API with a hundred distinct statuses
		// would otherwise turn one counter into a hundred time series, and
		// the question an operator asks is "are calls failing".
		c.statuses[fmt.Sprintf("%dxx", event.Status/100)]++
	}
	seconds := float64(event.DurationMs) / 1000
	c.durationSeconds += seconds
	c.durationCount++
	for i, bound := range durationBounds {
		if seconds <= bound {
			c.durationBuckets[i]++
			break
		}
	}
}

// Snapshot is what the runtime knows about itself at scrape time, beyond what
// the events say.
type Snapshot struct {
	Tools  int
	Digest string
	// CallsInFlight and CallLimit are the concurrency limit's state. A zero
	// limit means there is none to report.
	CallsInFlight int
	CallLimit     int
}

// Handler serves the Prometheus text exposition format.
//
// snapshot may be nil, in which case the catalog gauges are omitted rather
// than reported as zero: "no catalog" and "a catalog with no tools" are
// different, and a dashboard cannot tell them apart after the fact.
func (c *Counters) Handler(snapshot func() Snapshot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var out strings.Builder
		c.write(&out, snapshot)
		_, _ = io.WriteString(w, out.String())
	})
}

func (c *Counters) write(out *strings.Builder, snapshot func() Snapshot) {
	info := buildinfo.Get()
	metric(out, "lotsman_build_info", "gauge", "Build identity of the running binary.")
	sample(out, "lotsman_build_info", map[string]string{"version": info.Version, "commit": info.Commit}, 1)

	if snapshot != nil {
		current := snapshot()
		metric(out, "lotsman_catalog_tools", "gauge", "Tools in the published catalog.")
		sample(out, "lotsman_catalog_tools", nil, float64(current.Tools))
		metric(out, "lotsman_catalog_info", "gauge", "Identity of the published catalog.")
		sample(out, "lotsman_catalog_info", map[string]string{"digest": current.Digest}, 1)
		if current.CallLimit > 0 {
			metric(out, "lotsman_calls_in_flight", "gauge", "Tool calls executing now.")
			sample(out, "lotsman_calls_in_flight", nil, float64(current.CallsInFlight))
			metric(out, "lotsman_calls_limit", "gauge", "Tool calls allowed in flight at once (execution.maxConcurrentCalls).")
			sample(out, "lotsman_calls_limit", nil, float64(current.CallLimit))
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	metric(out, "lotsman_tool_calls_total", "counter", "Tool calls by how they ended.")
	for _, decision := range sortedKeys(c.decisions) {
		sample(out, "lotsman_tool_calls_total",
			map[string]string{"decision": string(decision)}, float64(c.decisions[decision]))
	}

	metric(out, "lotsman_refusals_total", "counter", "Refused calls by which control refused them.")
	for _, reason := range sortedKeys(c.refusals) {
		sample(out, "lotsman_refusals_total", map[string]string{"reason": reason}, float64(c.refusals[reason]))
	}

	metric(out, "lotsman_upstream_responses_total", "counter", "Upstream responses by status class.")
	for _, class := range sortedKeys(c.statuses) {
		sample(out, "lotsman_upstream_responses_total", map[string]string{"class": class}, float64(c.statuses[class]))
	}

	metric(out, "lotsman_call_duration_seconds", "histogram", "Time from tool call to outcome.")
	var cumulative uint64
	for i, bound := range durationBounds {
		cumulative += c.durationBuckets[i]
		sample(out, "lotsman_call_duration_seconds_bucket",
			map[string]string{"le": strconv.FormatFloat(bound, 'g', -1, 64)}, float64(cumulative))
	}
	sample(out, "lotsman_call_duration_seconds_bucket", map[string]string{"le": "+Inf"}, float64(c.durationCount))
	sample(out, "lotsman_call_duration_seconds_sum", nil, c.durationSeconds)
	sample(out, "lotsman_call_duration_seconds_count", nil, float64(c.durationCount))
}

// sortedKeys keeps a scrape byte-identical between two scrapes that saw the
// same events. Map order would make a diff of two scrapes unreadable, and
// determinism is cheap here (Principle IV).
func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func metric(out *strings.Builder, name, kind, help string) {
	fmt.Fprintf(out, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

func sample(out *strings.Builder, name string, labels map[string]string, value float64) {
	out.WriteString(name)
	if len(labels) > 0 {
		out.WriteByte('{')
		for i, key := range sortedKeys(labels) {
			if i > 0 {
				out.WriteByte(',')
			}
			// Quoted by hand: %q would escape the escapes.
			fmt.Fprintf(out, `%s="%s"`, key, escapeLabel(labels[key]))
		}
		out.WriteByte('}')
	}
	fmt.Fprintf(out, " %g\n", value)
}

// escapeLabel applies the exposition format's three escapes. Every label
// value here is build metadata or a digest, but a value that came from
// somewhere else one day must not be able to forge a sample line.
func escapeLabel(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(value)
}
