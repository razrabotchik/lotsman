package mcpserver_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// `body: null` is the JSON value null. Where the schema allows it, it is sent
// as one; where it does not, the validator refuses it before the network --
// it is never quietly turned into "no body".
func TestANullBodyIsSentOnlyWhereTheSchemaAllowsIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		schema   domain.Schema
		required bool
		wantSent bool
	}{
		{"required and nullable", domain.Schema{"type": []any{"object", "null"}}, true, true},
		{"optional and nullable", domain.Schema{"type": []any{"object", "null"}}, false, true},
		{"optional and not nullable", domain.Schema{"type": "object"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, uri, body := recordingAPI(t)
			op := domain.Operation{
				Key: "ns:PUT:/settings", Method: "PUT", PathTemplate: "/settings",
				Servers: []string{api.URL},
				Effect:  domain.EffectDecision{Effect: domain.EffectWrite, Source: domain.EffectSourceHTTPMethod},
				Input: domain.InputModel{Body: &domain.BodySpec{
					MediaType: "application/json", Required: tc.required, Schema: tc.schema,
				}},
				Support: domain.SupportStatus{Level: domain.SupportSupported},
			}
			cat := catalog.Build("sha256:test", []domain.Operation{op},
				catalog.Options{Policy: policy.Config{AllowMutations: true}})
			session := connect(t, &mcpserver.Options{
				Catalog: &cat, BaseURL: api.URL, HTTPClient: api.Client(), Approval: config.ApprovalNever,
			})

			res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: cat.Tools[0].Name, Arguments: map[string]any{"body": nil},
			})
			sent := *uri != ""
			if sent != tc.wantSent {
				t.Fatalf("sent = %v, want %v (%q)", sent, tc.wantSent, errorText(t, res, err))
			}
			if sent && *body != "null" {
				t.Errorf("the API received the body %q, want null", *body)
			}
			if !sent && err == nil && !res.IsError {
				t.Error("a refused call reported success")
			}
		})
	}
}
