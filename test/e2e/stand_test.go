package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const standSpec = "../../cmd/api-gateway/openapi.yaml"

// The development API exists so the runtime can be watched doing what its
// documents say. This walks the scenarios its README promises, through a real
// session against the real fixture, so that a scenario which stopped showing
// what it claims fails here rather than in front of somebody trying it.
func TestTheDevelopmentAPIShowsWhatTheRuntimeDoes(t *testing.T) {
	t.Parallel()
	origin := startDevelopmentAPI(t)
	config := writeConfig(t, `operationOverrides:
  - match: { operationId: createCharge }
    effect: write
`)
	session, stderr := stdioSession(t, standSpec, "--lax", "--config", config,
		"--base-url", origin, "--allow-mutations", "--approval", "never")

	call := func(name string, arguments map[string]any) (*mcp.CallToolResult, string) {
		t.Helper()
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatalf("%s: %v\nstderr:\n%s", name, err, stderr.String())
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		return res, string(raw)
	}

	t.Run("numbers arrive as written, and past int64 they are refused", func(t *testing.T) {
		_, raw := call("mirror_request", map[string]any{
			"path": map[string]any{"segment": "n"}, "query": map[string]any{"n": json.Number("9007199254740993")},
		})
		if !strings.Contains(raw, `n=9007199254740993`) {
			t.Errorf("the mirror did not see the number sent:\n%s", raw)
		}
		res, raw := call("mirror_request", map[string]any{
			"path": map[string]any{"segment": "n"}, "query": map[string]any{"n": json.Number("9223372036854775808")},
		})
		if !res.IsError || strings.Contains(raw, "rawQuery") {
			t.Errorf("a number past int64's maximum reached the API:\n%s", raw)
		}
	})

	t.Run("a null body is sent as null", func(t *testing.T) {
		_, raw := call("mirror_body", map[string]any{"path": map[string]any{"segment": "x"}, "body": nil})
		if !strings.Contains(raw, `\"body\":\"null\"`) {
			t.Errorf("the mirror did not receive a null body:\n%s", raw)
		}
	})

	t.Run("an answer that never came is not a refusal", func(t *testing.T) {
		res, raw := call("create_charge", map[string]any{"body": map[string]any{"amount": 500}})
		if !res.IsError || !strings.Contains(raw, "may have acted") {
			t.Errorf("the caller was not told the charge may have happened:\n%s", raw)
		}
		_, listed := call("list_charges", nil)
		if !strings.Contains(listed, `\"count\":1`) {
			t.Errorf("the charge was not made, so the scenario shows nothing:\n%s", listed)
		}
	})

	t.Run("a status with half a body is an answer", func(t *testing.T) {
		res, raw := call("get_truncated_body", nil)
		if !res.IsError || !strings.Contains(raw, "answered 200") {
			t.Errorf("the caller was not told the API answered:\n%s", raw)
		}
	})

	t.Run("an operation named ping does not replace the built-in", func(t *testing.T) {
		listed, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var builtIn, operation bool
		for _, tool := range listed.Tools {
			builtIn = builtIn || tool.Name == "ping"
			operation = operation || strings.HasPrefix(tool.Name, "ping_")
		}
		if !builtIn || !operation {
			t.Errorf("built-in ping %v, operation ping_<hash> %v; want both", builtIn, operation)
		}
	})
}

// A rotated client secret takes effect at the next mint, and every token
// going stale at once costs one new one -- the two OAuth scenarios the
// fixture's /oauth/stats makes countable.
func TestTheDevelopmentAPIShowsRotationAndOneMintPerStaleToken(t *testing.T) {
	t.Parallel()
	api := startDevelopmentAPIWith(t, true)
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	writeSecret := func(value string) {
		t.Helper()
		if err := os.WriteFile(secretFile, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSecret("demo-client-secret")
	config := writeConfig(t, `execution:
  allowedOrigins: ["`+api.origin+`", "`+api.tlsOrigin+`"]
authProfiles:
  serviceAuth:
    scheme: oauth2-client-credentials
    tokenURL: `+api.tlsOrigin+`/oauth/token
    clientID: demo-client
    clientSecretRef: file:`+secretFile+`
`)
	session, stderr := stdioSessionEnv(t, []string{"SSL_CERT_FILE=" + api.certificate},
		standSpec, "--lax", "--config", config, "--base-url", api.origin)

	profile := func() {
		t.Helper()
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_minted_profile"})
		if err != nil || res.IsError {
			t.Fatalf("get_minted_profile: %v %+v\nstderr:\n%s", err, res, stderr.String())
		}
	}
	stats := func() (mints int, lastSecret string) {
		t.Helper()
		res, err := http.Get(api.origin + "/oauth/stats")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got struct {
			Mints      int    `json:"mints"`
			LastSecret string `json:"lastSecret"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got.Mints, got.LastSecret
	}

	profile()
	if mints, secret := stats(); mints != 1 || secret != "current" {
		t.Fatalf("after the first call: %d mints with the %q secret", mints, secret)
	}

	// Rotate the secret, then make every issued token stale at once.
	writeSecret("demo-client-secret-next")
	res, err := http.Post(api.origin+"/oauth/revoke", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	var wait sync.WaitGroup
	for range 10 {
		wait.Go(profile)
	}
	wait.Wait()
	if mints, secret := stats(); mints != 2 || secret != "next" {
		t.Errorf("ten calls on one revoked token: %d mints in total with the %q secret; want 2 and next",
			mints, secret)
	}
}

// The fixture's README counts its operations, and a count in prose rots: it
// said 38 and 33 while the document had grown to 39 and 34. It is held to
// what inspect reports.
func TestTheDevelopmentAPIReadmeCountsItsOperations(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runCLI(t, "inspect", standSpec, "--json")
	if code != 0 {
		t.Fatalf("inspect exited %d: %s", code, stderr)
	}
	var report struct {
		Totals struct {
			Found, Supported, Rejected int
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := fmt.Sprintf("The document declares %d operations. Lotsman supports %d of them, refuses %d,",
		report.Totals.Found, report.Totals.Supported, report.Totals.Rejected)
	readme := strings.Join(strings.Fields(readRepoFile(t, "cmd/api-gateway/README.md")), " ")
	if !strings.Contains(readme, want) {
		t.Errorf("cmd/api-gateway/README.md does not say %q", want)
	}
}
