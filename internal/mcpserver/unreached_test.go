package mcpserver_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/audit"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// Every call of an operation leaves exactly one record, however early it was
// refused -- by the SDK's schema check, for a tool that does not exist, for
// an id search mode cannot resolve. The tools that only describe leave none.
func TestEveryOperationCallIsRecordedOnce(t *testing.T) {
	api, _, _ := recordingAPI(t)
	type want struct {
		decision  audit.Decision
		reason    string
		operation string
	}
	for _, mode := range []catalog.Mode{catalog.ModeTools, catalog.ModeSearch} {
		t.Run(string(mode), func(t *testing.T) {
			cat := catalog.Build("sha256:test", searchOperations(api.URL),
				catalog.Options{Mode: mode, Policy: policy.Config{AllowMutations: true}})
			events := &recorder{}
			session := connect(t, &mcpserver.Options{
				Catalog: &cat, BaseURL: api.URL, HTTPClient: api.Client(),
				Approval: config.ApprovalNever, Audit: events,
			})
			listPets, createPet := "list_pets", "create_pet"

			calls := []struct {
				name      string
				arguments map[string]any
				want      *want
			}{
				{"ping", nil, nil},
				{"an executed read", nil, &want{audit.DecisionExecuted, "", "ns:GET:/pets"}},
				{"arguments the schema rejects", map[string]any{"body": map[string]any{"name": 7}},
					&want{audit.DecisionRefused, "usage", "ns:POST:/pets"}},
				{"a tool or id that does not exist", nil, &want{audit.DecisionRefused, "usage", ""}},
			}
			call := func(name string, arguments map[string]any) {
				_, _ = session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
			}
			for _, c := range calls {
				before := events.count()
				switch {
				case c.name == "ping":
					call("ping", nil)
				case mode == catalog.ModeTools && c.want.operation == "ns:GET:/pets":
					call(listPets, c.arguments)
				case mode == catalog.ModeTools && c.want.operation == "ns:POST:/pets":
					call(createPet, c.arguments)
				case mode == catalog.ModeTools:
					call("no_such_tool", nil)
				case c.want.operation == "ns:GET:/pets":
					call("call_read_operation", map[string]any{"id": "ns:GET:/pets"})
				case c.want.operation == "ns:POST:/pets":
					call("call_mutating_operation", map[string]any{"id": "ns:POST:/pets", "arguments": c.arguments})
				default:
					call("call_read_operation", map[string]any{"id": "ns:GET:/nope"})
				}
				got := events.since(before)
				if c.want == nil {
					if len(got) != 0 {
						t.Errorf("%s: recorded %d events, want none", c.name, len(got))
					}
					continue
				}
				if len(got) != 1 {
					t.Errorf("%s: recorded %d events, want exactly one", c.name, len(got))
					continue
				}
				if got[0].Decision != c.want.decision || got[0].Reason != c.want.reason || got[0].Operation != c.want.operation {
					t.Errorf("%s: recorded %s/%q/%q, want %s/%q/%q", c.name,
						got[0].Decision, got[0].Reason, got[0].Operation,
						c.want.decision, c.want.reason, c.want.operation)
				}
			}

			if mode == catalog.ModeSearch {
				before := events.count()
				call("search_operations", map[string]any{"query": "pets"})
				call("describe_operation", map[string]any{"id": "ns:GET:/pets"})
				// The effect gate keeps its own class, recorded once.
				call("call_read_operation", map[string]any{"id": "ns:POST:/pets"})
				got := events.since(before)
				if len(got) != 1 || got[0].Reason != "policy" {
					t.Errorf("search, describe and a gated call recorded %+v, want one policy refusal", got)
				}
			}
		})
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recorder) since(n int) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Event(nil), r.events[n:]...)
}
