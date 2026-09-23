package inbound

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/errs"
)

const canary = "CANARY-inbound-2f81-DO-NOT-LEAK"

// With no inbound authorization the guard says so, and says it in the field
// the bind rule reads (FR-70): "nothing authenticates this" has to be a fact
// another package can act on, not a comment.
func TestNoneAuthenticatesNothingAndAdmitsIt(t *testing.T) {
	guard, err := New(&config.InboundAuth{}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if guard.Required {
		t.Error("the default guard claims to require authentication")
	}

	var reached bool
	guard.Middleware(spy(&reached)).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody))
	if !reached {
		t.Error("mode none refused a request")
	}
}

// FR-79: a shared secret admits the caller who has it and nobody else.
func TestStaticBearer(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_INBOUND", canary)
	guard, err := New(&config.InboundAuth{
		Mode:     config.InboundStaticBearer,
		TokenRef: config.SecretRef("env:LOTSMAN_TEST_INBOUND"),
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !guard.Required {
		t.Fatal("a configured bearer guard does not report itself as required")
	}

	cases := []struct {
		name      string
		authorize string
		want      int
	}{
		{name: "the configured token", authorize: "Bearer " + canary, want: http.StatusOK},
		{name: "no token at all", want: http.StatusUnauthorized},
		{name: "a different token", authorize: "Bearer not-the-token", want: http.StatusUnauthorized},
		{name: "the token without the scheme", authorize: canary, want: http.StatusUnauthorized},
		{name: "a prefix of the token", authorize: "Bearer " + canary[:10], want: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
			if tc.authorize != "" {
				req.Header.Set("Authorization", tc.authorize)
			}
			rec := httptest.NewRecorder()
			guard.Middleware(spy(&reached)).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want != http.StatusOK && reached {
				t.Error("an unauthenticated request reached the handler")
			}
		})
	}
}

// FR-84: the refusal names the scheme that would satisfy it and nothing else.
// A challenge that distinguished "no token" from "wrong token", or named the
// deployment, would tell anyone who can reach the port how it is configured.
func TestTheChallengeSaysHowWithoutSayingWhy(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_INBOUND", canary)
	guard, err := New(&config.InboundAuth{
		Mode:     config.InboundStaticBearer,
		TokenRef: config.SecretRef("env:LOTSMAN_TEST_INBOUND"),
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, authorize := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", http.NoBody)
		if authorize != "" {
			req.Header.Set("Authorization", authorize)
		}
		rec := httptest.NewRecorder()
		guard.Middleware(spy(new(bool))).ServeHTTP(rec, req)

		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("WWW-Authenticate = %q, want Bearer", got)
		}
		if body := rec.Body.String(); strings.Contains(body, canary) {
			t.Errorf("the refusal echoed the configured token: %q", body)
		}
	}
}

// A secret that resolves to nothing must not become an endpoint that accepts
// everyone. It is a startup failure, while someone is still watching.
func TestAnEmptyTokenIsAStartupFailure(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_INBOUND", "")
	_, err := New(&config.InboundAuth{
		Mode:     config.InboundStaticBearer,
		TokenRef: config.SecretRef("env:LOTSMAN_TEST_INBOUND"),
	}, nil, nil)
	if err == nil {
		t.Fatal("an empty inbound token was accepted")
	}
}

// A mode nobody defines is refused rather than read as `none`: the
// difference between an operator who knows their endpoint is unauthenticated
// and one who believes it is not.
func TestAnUnknownModeIsRefusedRatherThanIgnored(t *testing.T) {
	guard, err := New(&config.InboundAuth{Mode: "mtls"}, nil, nil)
	if err == nil {
		t.Fatal("an unknown inbound mode was accepted")
	}
	if errs.ClassOf(err) != errs.ClassUsage {
		t.Errorf("class = %s, want usage", errs.ClassOf(err))
	}
	if guard.Required {
		t.Error("a guard that failed to build claims to require authentication")
	}
}

// discardLogger is the logger a test that is not about logging wants.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func spy(reached *bool) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *reached = true })
}
