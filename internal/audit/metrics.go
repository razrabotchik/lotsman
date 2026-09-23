package audit

import (
	"fmt"
	"io"
	"net/http"
	"sort"
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
}

// NewCounters returns an empty tally.
func NewCounters() *Counters {
	return &Counters{
		decisions: map[Decision]uint64{},
		refusals:  map[string]uint64{},
		statuses:  map[string]uint64{},
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
	c.durationSeconds += float64(event.DurationMs) / 1000
	c.durationCount++
}

// Snapshot is what the runtime knows about itself at scrape time, beyond what
// the events say.
type Snapshot struct {
	Tools  int
	Digest string
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

	metric(out, "lotsman_call_duration_seconds", "summary", "Time from tool call to outcome.")
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
