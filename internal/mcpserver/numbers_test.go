package mcpserver_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// Numbers past 2^53 have no float64 of their own. Before this was fixed,
// 9007199254740993 reached the API as 9007199254740992 -- a different
// account, sent without an error -- because the arguments were decoded into
// float64 on the way in. These are the values that tell exact from rounded,
// in every place a number can travel.
const (
	pastFloat     = "9007199254740993"
	maxInt64      = "9223372036854775807"
	longDecimal   = "0.1000000000000000055511151231257827"
	wantURI       = "/accounts/" + pastFloat + "?limit=" + maxInt64
	wantBodyField = `"amount":` + longDecimal
)

func accountOperation(server string) domain.Operation {
	integer := domain.Schema{"type": "integer"}
	return domain.Operation{
		Key: "ns:POST:/accounts/{id}", Method: "POST", PathTemplate: "/accounts/{id}",
		SourceOperationID: "creditAccount", Summary: "creditAccount",
		Servers: []string{server},
		Effect:  domain.EffectDecision{Effect: domain.EffectWrite, Source: domain.EffectSourceHTTPMethod},
		Input: domain.InputModel{
			Parameters: []domain.Parameter{
				{Name: "id", In: domain.LocationPath, Required: true, Style: domain.StyleSimple, Schema: integer},
				// The bound the openapi adapter publishes for int64's limit: a
				// literal, which the SDK's own schema handling has to accept.
				{Name: "limit", In: domain.LocationQuery, Style: domain.StyleForm, Explode: true, Schema: domain.Schema{
					"type": "integer", "maximum": json.Number(maxInt64),
				}},
			},
			Body: &domain.BodySpec{MediaType: "application/json", Required: true, Schema: domain.Schema{
				"type":                 "object",
				"properties":           map[string]any{"amount": map[string]any{"type": "number"}},
				"additionalProperties": false,
			}},
		},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
}

// recordingAPI keeps the request line and the body it received.
func recordingAPI(t *testing.T) (server *httptest.Server, uri, body *string) {
	t.Helper()
	uri, body = new(string), new(string)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*uri, *body = r.URL.RequestURI(), string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server, uri, body
}

// exactArguments are the grouped arguments, numbers as the literals a client
// sends.
func exactArguments() map[string]any {
	return map[string]any{
		"path":  map[string]any{"id": json.Number(pastFloat)},
		"query": map[string]any{"limit": json.Number(maxInt64)},
		"body":  map[string]any{"amount": json.Number(longDecimal)},
	}
}

func checkExact(t *testing.T, res *mcp.CallToolResult, err error, uri, body string) {
	t.Helper()
	if err != nil || res.IsError {
		t.Fatalf("call failed: %v %q", err, errorText(t, res, err))
	}
	if uri != wantURI {
		t.Errorf("the API received %s, want %s", uri, wantURI)
	}
	if want := "{" + wantBodyField + "}"; body != want {
		t.Errorf("the API received the body %s, want %s", body, want)
	}
}

func TestNumbersReachTheAPIExactlyInToolsMode(t *testing.T) {
	api, uri, body := recordingAPI(t)
	cat := catalog.Build("sha256:test", []domain.Operation{accountOperation(api.URL)},
		catalog.Options{Policy: policy.Config{AllowMutations: true}})
	session := connect(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: api.URL, HTTPClient: api.Client(), Approval: config.ApprovalNever,
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: cat.Tools[0].Name, Arguments: exactArguments(),
	})
	checkExact(t, res, err, *uri, *body)
}

// Search mode is a second front door onto the same runner, and it decodes
// its own input: it has to keep the numbers as well.
func TestNumbersReachTheAPIExactlyInSearchMode(t *testing.T) {
	api, uri, body := recordingAPI(t)
	cat := catalog.Build("sha256:test", []domain.Operation{accountOperation(api.URL)},
		catalog.Options{Mode: catalog.ModeSearch, Policy: policy.Config{AllowMutations: true}})
	session := connect(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: api.URL, HTTPClient: api.Client(), Approval: config.ApprovalNever,
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "call_mutating_operation",
		Arguments: map[string]any{
			"id":        "ns:POST:/accounts/{id}",
			"arguments": exactArguments(),
		},
	})
	checkExact(t, res, err, *uri, *body)
}

// An exact value costs exact arithmetic, and a short literal can denote an
// enormous one. The bound is checked before the validator does that
// arithmetic, and the API never hears about it.
func TestANumberBeyondTheCarriedBoundsIsRefused(t *testing.T) {
	api, uri, _ := recordingAPI(t)
	cat := catalog.Build("sha256:test", []domain.Operation{accountOperation(api.URL)},
		catalog.Options{Policy: policy.Config{AllowMutations: true}})
	session := connect(t, &mcpserver.Options{
		Catalog: &cat, BaseURL: api.URL, HTTPClient: api.Client(), Approval: config.ApprovalNever,
	})

	arguments := exactArguments()
	arguments["body"] = map[string]any{"amount": json.Number("1e-100000000")}
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: cat.Tools[0].Name, Arguments: arguments})
	if err == nil && !res.IsError {
		t.Fatal("a number past the bounds was carried")
	}
	if *uri != "" {
		t.Errorf("the API was called: %s", *uri)
	}
}
