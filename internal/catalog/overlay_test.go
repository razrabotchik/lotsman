package catalog

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/policy"
)

func inferredPost(key, path, opID string) domain.Operation {
	op := supportedOp(key, "POST", path, opID)
	op.Effect = domain.EffectDecision{
		Effect:     domain.EffectUnknown,
		Source:     domain.EffectSourceHTTPMethod,
		Confidence: domain.ConfidenceInferred,
	}
	return op
}

func enabled(v bool) *bool { return &v }

func TestOverrideStatesTheEffectExplicitly(t *testing.T) {
	ops := []domain.Operation{inferredPost("ns:POST:/search", "/search", "searchPets")}
	cat := Build("d", ops, Options{Overrides: []config.OperationOverride{
		{Match: config.Match{OperationID: "searchPets"}, Effect: "read"},
	}})

	if len(cat.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(cat.Tools))
	}
	tool := cat.Tools[0]
	// A POST that the operator reviewed and called a read executes under the
	// default policy: that is the whole point of an explicit classification.
	if !tool.Executable || len(tool.PolicyBlockers) != 0 {
		t.Errorf("overridden read = %+v, want executable under read-only policy", tool)
	}
	if tool.Effect.Source != domain.EffectSourceLocalOverride || tool.Effect.Confidence != domain.ConfidenceExplicit {
		t.Errorf("provenance = %s/%s, want local_override/explicit", tool.Effect.Source, tool.Effect.Confidence)
	}
	// The report must carry the same provenance: an operator reading it has
	// to be able to tell a heuristic from their own decision.
	verdict := cat.Report.Operations[0]
	if verdict.Effect.Source != domain.EffectSourceLocalOverride {
		t.Errorf("report effect source = %s, want local_override", verdict.Effect.Source)
	}
}

func TestOverrideDoesNotMutateTheCallersOperations(t *testing.T) {
	ops := []domain.Operation{inferredPost("ns:POST:/search", "/search", "searchPets")}
	Build("d", ops, Options{Overrides: []config.OperationOverride{
		{Match: config.Match{OperationID: "searchPets"}, Effect: "read"},
	}})

	if ops[0].Effect.Effect != domain.EffectUnknown || ops[0].Effect.Source != domain.EffectSourceHTTPMethod {
		t.Fatalf("Build mutated the caller's IR: %+v", ops[0].Effect)
	}
}

func TestOverrideCanRaiseAnEffectToo(t *testing.T) {
	op := supportedOp("ns:GET:/exports", "GET", "/exports", "listExports")
	op.Effect = domain.EffectDecision{Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred}

	cat := Build("d", []domain.Operation{op}, Options{Overrides: []config.OperationOverride{
		{Match: config.Match{Method: "get", Path: "/exports"}, Effect: "destructive"},
	}})

	tool := cat.Tools[0]
	if tool.Executable || len(tool.PolicyBlockers) == 0 {
		t.Errorf("a GET declared destructive = %+v, want refused under read-only policy", tool)
	}
}

func TestDisabledOverrideRemovesTheOperationAndSaysSo(t *testing.T) {
	ops := []domain.Operation{
		supportedOp("ns:GET:/pets", "GET", "/pets", "listPets"),
		supportedOp("ns:GET:/legacy", "GET", "/legacy", "legacy"),
	}
	cat := Build("d", ops, Options{Overrides: []config.OperationOverride{
		{Match: config.Match{OperationID: "legacy"}, Enabled: enabled(false)},
	}})

	if len(cat.Tools) != 1 || cat.Tools[0].OperationKey != "ns:GET:/pets" {
		t.Fatalf("tools = %+v, want only the enabled operation", cat.Tools)
	}
	if cat.Report.Totals.Excluded != 1 {
		t.Errorf("Totals.Excluded = %d, want 1", cat.Report.Totals.Excluded)
	}
	if cat.Report.ByReason[domain.ReasonDisabledByOverride] != 1 {
		t.Errorf("byReason = %+v, want one disabled_by_override", cat.Report.ByReason)
	}
	// An operation removed from the surface is still accounted for: a filter
	// that hides its own effect is worse than no filter.
	var found bool
	for _, verdict := range cat.Report.Operations {
		if verdict.Key == "ns:GET:/legacy" {
			found = true
			if verdict.Published || !verdict.Excluded {
				t.Errorf("verdict = %+v, want excluded and unpublished", verdict)
			}
		}
	}
	if !found {
		t.Error("the disabled operation is missing from the report entirely")
	}
}

func TestIncludeTagsSelectsThePublishedSurface(t *testing.T) {
	droplets := supportedOp("ns:GET:/droplets", "GET", "/droplets", "listDroplets")
	droplets.Tags = []string{"droplets"}
	billing := supportedOp("ns:GET:/invoices", "GET", "/invoices", "listInvoices")
	billing.Tags = []string{"billing"}
	untagged := supportedOp("ns:GET:/health", "GET", "/health", "health")

	cat := Build("d", []domain.Operation{droplets, billing, untagged}, Options{IncludeTags: []string{"droplets"}})

	if len(cat.Tools) != 1 || cat.Tools[0].OperationKey != "ns:GET:/droplets" {
		t.Fatalf("tools = %+v, want only the droplets operation", cat.Tools)
	}
	// An untagged operation is out when a filter is configured: the operator
	// named a surface, and "untagged" is not on it.
	if cat.Report.Totals.Excluded != 2 || cat.Report.ByReason[domain.ReasonExcludedBySelection] != 2 {
		t.Errorf("totals = %+v, byReason = %+v, want two excluded_by_selection",
			cat.Report.Totals, cat.Report.ByReason)
	}
	if cat.Report.Totals.Published != 1 {
		t.Errorf("Published = %d, want 1", cat.Report.Totals.Published)
	}
}

func TestSelectionMatchesTheTagVocabularyTheReportShows(t *testing.T) {
	// The document's tag carries markup and whitespace; the catalog publishes
	// the sanitized form, and that is the only form an operator can see.
	op := supportedOp("ns:GET:/droplets", "GET", "/droplets", "listDroplets")
	op.Tags = []string{"  drop<b>lets  "}

	cat := Build("d", []domain.Operation{op}, Options{IncludeTags: []string{"droplets"}})
	if len(cat.Tools) != 1 {
		t.Fatalf("tools = %+v, want the filter to match the sanitized tag", cat.Tools)
	}
}

func TestSelectionAndOverridesChangeTheDigest(t *testing.T) {
	ops := []domain.Operation{
		supportedOp("ns:GET:/pets", "GET", "/pets", "listPets"),
		supportedOp("ns:GET:/legacy", "GET", "/legacy", "legacy"),
	}
	full := Build("d", ops, Options{})
	filtered := Build("d", ops, Options{Overrides: []config.OperationOverride{
		{Match: config.Match{OperationID: "legacy"}, Enabled: enabled(false)},
	}})

	if full.Digest == filtered.Digest {
		t.Error("a catalog with a different published surface must have a different digest")
	}
}

func TestOverrideCannotPublishAnUnsupportedOperation(t *testing.T) {
	rejected := domain.Operation{
		Key:               "ns:POST:/forms",
		SourceOperationID: "postForm",
		Method:            "POST",
		PathTemplate:      "/forms",
		Support:           domain.SupportStatus{Level: domain.SupportRejected, Reasons: []domain.ReasonCode{domain.ReasonUnsupportedMediaType}},
	}
	cat := Build("d", []domain.Operation{rejected}, Options{
		Policy:    policy.Config{AllowMutations: true},
		Overrides: []config.OperationOverride{{Match: config.Match{OperationID: "postForm"}, Effect: "read"}},
	})

	// Translation is not a matter of opinion: an override classifies, it does
	// not make lotsman able to serialize what it refused.
	if len(cat.Tools) != 0 {
		t.Errorf("tools = %+v, want none: an override cannot publish a rejected operation", cat.Tools)
	}
}

func TestValidateOverridesRefusesAnOverrideThatMatchesNothing(t *testing.T) {
	ops := []domain.Operation{supportedOp("ns:GET:/pets", "GET", "/pets", "listPets")}

	err := ValidateOverrides(ops, []config.OperationOverride{
		{Match: config.Match{OperationID: "listPetz"}, Effect: "read"},
	})
	if err == nil {
		t.Fatal("an override matching no operation was accepted")
	}
	if !strings.Contains(err.Error(), "listPetz") {
		t.Errorf("error = %v, want it to name the override that did not apply", err)
	}
}

func TestValidateOverridesRefusesACollidingOperationID(t *testing.T) {
	// docs/spec.md 5.2: two operations sharing an operationId cannot be told
	// apart, and applying the override to both would be a guess about which
	// one the operator reviewed.
	ops := []domain.Operation{
		supportedOp("ns:GET:/a", "GET", "/a", "duplicate"),
		supportedOp("ns:GET:/b", "GET", "/b", "duplicate"),
	}
	if err := ValidateOverrides(ops, []config.OperationOverride{
		{Match: config.Match{OperationID: "duplicate"}, Enabled: enabled(false)},
	}); err == nil {
		t.Fatal("an override matching two operations was accepted")
	}
}

func TestValidateOverridesRefusesTwoOverridesForOneOperation(t *testing.T) {
	ops := []domain.Operation{supportedOp("ns:GET:/pets", "GET", "/pets", "listPets")}
	if err := ValidateOverrides(ops, []config.OperationOverride{
		{Match: config.Match{OperationID: "listPets"}, Effect: "read"},
		{Match: config.Match{Method: "GET", Path: "/pets"}, Enabled: enabled(false)},
	}); err == nil {
		t.Fatal("two overrides for one operation were accepted; precedence must not be invented")
	}
}

func TestValidateOverridesAcceptsWhatBuildWillApply(t *testing.T) {
	ops := []domain.Operation{inferredPost("ns:POST:/search", "/search", "searchPets")}
	if err := ValidateOverrides(ops, []config.OperationOverride{
		{Match: config.Match{Method: "POST", Path: "/search"}, Effect: "read"},
	}); err != nil {
		t.Fatalf("a matching override was refused: %v", err)
	}
}

func TestAuthProfilePinIsAppliedPerOperation(t *testing.T) {
	profiles := auth.NewProfiles(map[string]config.Profile{
		"bearerAuth": {Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
		"personal":   {Scheme: config.SchemeBearer, TokenRef: "env:PERSONAL", Satisfies: []string{"bearerAuth"}},
	})
	secured := func(key, path, opID string) domain.Operation {
		op := supportedOp(key, "GET", path, opID)
		op.Effect = domain.EffectDecision{Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred}
		op.Security = []domain.SecurityAlternative{{Requirements: []domain.SecurityRequirement{
			{Scheme: "bearerAuth", Type: "http", HTTP: "bearer", Satisfiable: true},
		}}}
		return op
	}
	ops := []domain.Operation{secured("ns:GET:/pets", "/pets", "listPets"), secured("ns:GET:/me", "/me", "getMe")}

	cat := Build("d", ops, Options{Auth: profiles, Overrides: []config.OperationOverride{
		{Match: config.Match{OperationID: "getMe"}, AuthProfile: "personal"},
	}})

	byKey := map[domain.OperationKey]Tool{}
	for _, tool := range cat.Tools {
		byKey[tool.OperationKey] = tool
	}
	// The pin decides one operation and leaves the other exactly as it was:
	// still ambiguous, still refused, still saying how to fix it.
	pinned := byKey["ns:GET:/me"]
	if !pinned.Executable || len(pinned.AuthProfiles) != 1 || pinned.AuthProfiles[0] != "personal" {
		t.Errorf("pinned operation = %+v, want it bound to the named profile", pinned)
	}
	unpinned := byKey["ns:GET:/pets"]
	if unpinned.Executable {
		t.Errorf("unpinned operation = %+v, want it still refused as ambiguous", unpinned)
	}
	if len(unpinned.ExecutionBlockers) == 0 || unpinned.ExecutionBlockers[len(unpinned.ExecutionBlockers)-1] != domain.ReasonAmbiguousSecurity {
		t.Errorf("blockers = %v, want ambiguous_security", unpinned.ExecutionBlockers)
	}
}
