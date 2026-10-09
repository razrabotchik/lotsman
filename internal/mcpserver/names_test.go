package mcpserver_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
)

// The catalog keeps operations off the built-in names only if it knows them
// all. Every tool lotsman registers itself, in either mode, must be on its
// list.
func TestEveryBuiltInToolNameIsReservedInTheCatalog(t *testing.T) {
	for _, mode := range []catalog.Mode{catalog.ModeTools, catalog.ModeSearch} {
		cat := catalog.Build("sha256:test", searchOperations("https://api.example.com"),
			catalog.Options{Mode: mode, Policy: policy.Config{AllowMutations: true}})
		session := connect(t, &mcpserver.Options{Catalog: &cat})
		res, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		operations := map[string]bool{}
		for _, tool := range cat.Tools {
			operations[tool.Name] = true
		}
		for _, tool := range res.Tools {
			if !operations[tool.Name] && !catalog.IsReservedToolName(tool.Name) {
				t.Errorf("%s mode registers %q, which the catalog may hand to an operation", mode, tool.Name)
			}
		}
	}
}

// A catalog not built by catalog.Build can still carry a duplicate. The SDK
// would replace one tool with the other in silence; the server refuses the
// duplicate and says so.
func TestADuplicateToolNameIsRefusedLoudly(t *testing.T) {
	tool := func(key domain.OperationKey, name string) catalog.Tool {
		return catalog.Tool{Name: name, OperationKey: key, Method: "GET", PathTemplate: "/x",
			Servers: []string{"https://api.example.com"}}
	}
	cat := catalog.Catalog{Tools: []catalog.Tool{
		tool("ns:GET:/a", "same"),
		tool("ns:GET:/b", "same"),
		tool("ns:GET:/ping", "ping"),
	}}
	var logged bytes.Buffer
	session := connect(t, &mcpserver.Options{
		Catalog: &cat, Logger: slog.New(slog.NewTextHandler(&logged, nil)),
	})
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	count := map[string]int{}
	for _, published := range res.Tools {
		count[published.Name]++
	}
	if count["same"] != 1 || count["ping"] != 1 {
		t.Errorf("published %v, want one of each", count)
	}
	for _, key := range []string{"ns:GET:/b", "ns:GET:/ping"} {
		if !strings.Contains(logged.String(), key) {
			t.Errorf("the refusal of %s was not logged:\n%s", key, logged.String())
		}
	}
}
