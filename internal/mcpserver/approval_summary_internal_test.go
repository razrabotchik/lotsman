package mcpserver

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// The summary in an approval prompt is the whole basis of the decision a person
// makes about a mutation, and every rule in it is a rule about what must *not*
// be on the screen (FR-46).
//
// Each of these was unexercised. Coverage said 55% of the function, and the
// missing half was the half where the rules live: the header and cookie names
// shown without values, the body reduced to field names, and the cap that keeps
// the prompt readable.
func TestTheApprovalSummaryShowsWhatItPromisesAndNothingElse(t *testing.T) {
	const secret = "CANARY-approval-summary-7f3c-DO-NOT-LEAK"
	redact.Default.Add(secret)

	summary := argumentSummary(map[string]any{
		domain.GroupPath:    map[string]any{"dropletId": 12345},
		domain.GroupQuery:   map[string]any{"force": true, "confirm": "yes"},
		domain.GroupHeaders: map[string]any{"X-Tenant": "acme", "Authorization": "Bearer " + secret},
		domain.GroupCookies: map[string]any{"session": secret},
		domain.GroupBody:    map[string]any{"name": "prod-db", "password": secret},
	})

	// Path and query values are shown: approving the deletion of "a droplet" is
	// not a decision, and these are what say which one.
	for _, want := range []string{"dropletId=12345", "confirm=yes", "force=true"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary does not show %q, so the prompt does not say what would happen:\n%s",
				want, summary)
		}
	}

	// Headers and cookies are named, never valued: that is where a credential
	// travels.
	for _, want := range []string{"headers: Authorization, X-Tenant", "cookies: session"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary does not name %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "acme") {
		t.Errorf("a header value reached the prompt:\n%s", summary)
	}

	// The body is field names only: it is unbounded, and it is the likeliest
	// place for something nobody wants on a screen.
	if !strings.Contains(summary, "body: name, password") {
		t.Errorf("the body is not summarized by its field names:\n%s", summary)
	}
	if strings.Contains(summary, "prod-db") {
		t.Errorf("a body value reached the prompt:\n%s", summary)
	}

	// And whatever survived the rules above still passes the redaction
	// registry, which is the backstop rather than the first line of defence.
	if strings.Contains(summary, secret) {
		t.Fatalf("a secret reached the prompt:\n%s", summary)
	}
}

// A body that is not an object still has to be announced. "No body" and "a body
// this summarizer could not describe" are different facts, and the second one
// must not read as the first.
func TestTheApprovalSummaryAnnouncesABodyItCannotSummarize(t *testing.T) {
	summary := argumentSummary(map[string]any{
		domain.GroupPath: map[string]any{"id": "7"},
		domain.GroupBody: []any{"a", "b"},
	})
	if !strings.Contains(summary, "body: present") {
		t.Errorf("an array body is not announced:\n%s", summary)
	}

	if got := argumentSummary(map[string]any{domain.GroupPath: map[string]any{"id": "7"}}); strings.Contains(got, "body") {
		t.Errorf("a call with no body claims one:\n%s", got)
	}
	// An empty group is not a line either: "query:" with nothing after it tells
	// a reader there was a query.
	if got := argumentSummary(map[string]any{domain.GroupQuery: map[string]any{}}); got != "" {
		t.Errorf("an empty group produced a line: %q", got)
	}
}

// A prompt nobody can read is a prompt everybody accepts, so the summary is
// capped -- and the cap has to be visible as a cut rather than look like the
// end of the arguments.
func TestTheApprovalSummaryIsBounded(t *testing.T) {
	values := map[string]any{}
	for i := range 200 {
		values[strings.Repeat("k", 8)+string(rune('a'+i%26))+strings.Repeat("0", i%7)] = strings.Repeat("v", 40)
	}
	summary := argumentSummary(map[string]any{domain.GroupQuery: values})

	if len(summary) <= approvalSummaryBytes {
		t.Fatalf("the fixture did not exceed the budget: %d bytes", len(summary))
	}
	if !strings.HasSuffix(summary, "…") {
		t.Errorf("a truncated summary does not say it was cut:\n%s", summary)
	}
	// The ellipsis is three bytes of UTF-8 on top of the budget; what matters is
	// that the text itself stops at the budget.
	if len(strings.TrimSuffix(summary, "…")) != approvalSummaryBytes {
		t.Errorf("the summary was cut to %d bytes, want %d",
			len(strings.TrimSuffix(summary, "…")), approvalSummaryBytes)
	}
}
