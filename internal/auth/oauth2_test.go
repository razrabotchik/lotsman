package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

const (
	clientSecret = "CANARY-client-secret-7b1f-DO-NOT-LEAK"
	mintedToken  = "CANARY-minted-token-4a93-DO-NOT-LEAK"
)

// tokenEndpoint is an authorization server that issues client-credentials
// tokens and counts how many it has issued.
type tokenEndpoint struct {
	server *httptest.Server
	mints  atomic.Int64
	// seen records the client authentication of the last request, so a test
	// can tell `client_secret_basic` from `client_secret_post`.
	mu        sync.Mutex
	basicAuth bool
	secret    string
	form      url.Values
	// answer overrides the default response.
	answer func(w http.ResponseWriter, r *http.Request)
}

func newTokenEndpoint(t *testing.T) *tokenEndpoint {
	t.Helper()
	e := &tokenEndpoint{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mints.Add(1)
		_ = r.ParseForm()
		e.mu.Lock()
		var password string
		_, password, e.basicAuth = r.BasicAuth()
		e.form = r.Form
		e.secret = password
		if !e.basicAuth {
			e.secret = r.Form.Get("client_secret")
		}
		answer := e.answer
		e.mu.Unlock()

		if answer != nil {
			answer(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": mintedToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(e.server.Close)
	return e
}

// profile is a minting profile pointed at this endpoint.
func (e *tokenEndpoint) profile(t *testing.T, scopes ...string) *config.Profile {
	t.Helper()
	t.Setenv("LOTSMAN_TEST_CLIENT_SECRET", clientSecret)
	return &config.Profile{
		Scheme:          config.SchemeOAuth2ClientCredentials,
		TokenURL:        e.server.URL,
		ClientID:        "lotsman",
		ClientSecretRef: config.SecretRef("env:LOTSMAN_TEST_CLIENT_SECRET"),
		Scopes:          scopes,
	}
}

// allowing is the egress policy an operator writes for this endpoint: its
// origin, named explicitly (ADR-0017).
func allowing(e *tokenEndpoint) *egress.Policy {
	return &egress.Policy{AllowedOrigins: []string{e.server.URL}}
}

// binding wraps a profile the way the catalog would.
func binding(name string, profile *config.Profile) Binding {
	return Binding{Credentials: []Credential{{
		Name:        name,
		Profile:     *profile,
		Requirement: domain.SecurityRequirement{Scheme: name, Type: "oauth2"},
	}}}
}

// FR-63 over the wire: the API receives a token lotsman obtained, not one it
// was given.
func TestAMintedTokenIsPresentedToTheAPI(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	var presented string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	client := Client(api.Client(), binding("api", endpoint.profile(t)), NewMinter(api.Client(), allowing(endpoint)))
	res, err := client.Get(api.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = res.Body.Close()

	if presented != "Bearer "+mintedToken {
		t.Errorf("the API received %q, want the minted token", presented)
	}
	if got := endpoint.mints.Load(); got != 1 {
		t.Errorf("minted %d times for one call, want 1", got)
	}
}

// US-2: a hundred calls do not mint a hundred tokens, and neither do a
// hundred concurrent ones.
func TestOneMintPerExpiryEvenUnderConcurrency(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	client := Client(api.Client(), binding("api", endpoint.profile(t)), NewMinter(api.Client(), allowing(endpoint)))

	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			res, err := client.Get(api.URL)
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			_ = res.Body.Close()
		}()
	}
	wait.Wait()

	if got := endpoint.mints.Load(); got != 1 {
		t.Errorf("twenty concurrent calls minted %d tokens, want 1", got)
	}
}

// The scopes and the audience the operator configured are what the
// authorization server is asked for.
func TestTheMintAsksForWhatWasConfigured(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	profile := endpoint.profile(t, "read", "write")
	profile.Audience = "https://api.example.com/"

	minter := NewMinter(endpoint.server.Client(), allowing(endpoint))
	credential := binding("api", profile).Credentials[0]
	if _, err := minter.token(t.Context(), &credential); err != nil {
		t.Fatalf("token: %v", err)
	}

	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if got := endpoint.form.Get("scope"); got != "read write" {
		t.Errorf("scope = %q, want both", got)
	}
	if got := endpoint.form.Get("resource"); got != "https://api.example.com/" {
		t.Errorf("resource = %q", got)
	}
	if got := endpoint.form.Get("grant_type"); got != "client_credentials" {
		t.Errorf("grant_type = %q", got)
	}
}

// FR-35's other half: dropping the cached token makes the next call mint.
func TestInvalidateForcesAFreshMint(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	minter := NewMinter(endpoint.server.Client(), allowing(endpoint))
	credential := binding("api", endpoint.profile(t)).Credentials[0]

	for range 3 {
		if _, err := minter.token(t.Context(), &credential); err != nil {
			t.Fatalf("token: %v", err)
		}
	}
	if got := endpoint.mints.Load(); got != 1 {
		t.Fatalf("minted %d times before invalidation, want 1", got)
	}

	minter.Invalidate("api")
	if _, err := minter.token(t.Context(), &credential); err != nil {
		t.Fatalf("token after invalidation: %v", err)
	}
	if got := endpoint.mints.Load(); got != 2 {
		t.Errorf("minted %d times in total, want 2", got)
	}
}

// A token endpoint that fails is a credential failure naming the profile and
// the origin -- not an upstream failure, because the API never heard about
// it. An operator reading this should be sent to the right system.
func TestATokenEndpointFailureIsACredentialFailure(t *testing.T) {
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter, r *http.Request)
	}{
		{name: "the provider refuses", answer: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		}},
		{name: "the provider answers with something else", answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>sign in</html>"))
		}},
		{name: "an empty access token", answer: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"","token_type":"Bearer","expires_in":3600}`))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := newTokenEndpoint(t)
			endpoint.answer = tc.answer

			var apiCalls atomic.Int64
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				apiCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer api.Close()

			client := Client(api.Client(), binding("api", endpoint.profile(t)), NewMinter(api.Client(), allowing(endpoint)))
			if _, err := client.Get(api.URL); err == nil {
				t.Fatal("a call proceeded without a token")
			} else if class := errs.ClassOf(err); class != errs.ClassAuth {
				t.Errorf("class = %s, want auth", class)
			} else if !strings.Contains(err.Error(), `"api"`) {
				t.Errorf("the failure does not name the profile: %v", err)
			}
			if got := apiCalls.Load(); got != 0 {
				t.Errorf("the API was called %d times despite having no credential", got)
			}
		})
	}
}

// Two secrets now live in one process. Neither may reach a log.
func TestNeitherTheClientSecretNorTheMintedTokenIsLogged(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	endpoint.answer = func(w http.ResponseWriter, _ *http.Request) {
		// The authorization server hands the client secret back as data --
		// the nastiest case, because it arrives as a value rather than a
		// credential.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + mintedToken + `","token_type":"Bearer","expires_in":3600}`))
	}

	minter := NewMinter(endpoint.server.Client(), allowing(endpoint))
	credential := binding("api", endpoint.profile(t)).Credentials[0]
	if _, err := minter.token(t.Context(), &credential); err != nil {
		t.Fatalf("token: %v", err)
	}

	var logged strings.Builder
	slog.New(redact.NewHandler(slog.NewTextHandler(&logged, nil))).
		Info("a line that quotes both", "secret", clientSecret, "token", mintedToken)

	if strings.Contains(logged.String(), clientSecret) {
		t.Errorf("the client secret reached the log:\n%s", logged.String())
	}
	if strings.Contains(logged.String(), mintedToken) {
		t.Errorf("the minted token reached the log:\n%s", logged.String())
	}
}

// ADR-0017: the token endpoint is egress. An origin the operator did not
// allowlist is refused before a connection is made, and the refusal is the
// policy's, so the operator is sent to the allowlist rather than the secret.
func TestATokenEndpointOutsideTheAllowlistIsNeverCalled(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	minter := NewMinter(endpoint.server.Client(), &egress.Policy{AllowedOrigins: []string{"https://api.example.com"}})
	credential := binding("api", endpoint.profile(t)).Credentials[0]

	_, err := minter.token(t.Context(), &credential)
	if err == nil {
		t.Fatal("a token endpoint outside the allowlist issued a token")
	}
	if !errors.Is(err, egress.ErrDenied) {
		t.Errorf("not an egress denial: %v", err)
	}
	if class := errs.ClassOf(err); class != errs.ClassPolicy {
		t.Errorf("class = %s, want policy", class)
	}
	if got := endpoint.mints.Load(); got != 0 {
		t.Errorf("the token endpoint was called %d times", got)
	}
}

// A rotated client secret is the one the next mint presents: the secret is
// read when a token is needed, not when the profile was first used.
func TestARotatedClientSecretTakesEffectAtTheNextMint(t *testing.T) {
	endpoint := newTokenEndpoint(t)
	endpoint.answer = func(w http.ResponseWriter, _ *http.Request) {
		// Expired on arrival, so every call mints.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + mintedToken + `","token_type":"Bearer","expires_in":1}`))
	}
	path := filepath.Join(t.TempDir(), "client-secret")
	profile := endpoint.profile(t)
	profile.ClientSecretRef = config.SecretRef("file:" + path)
	profile.AuthStyle = config.AuthStyleBody
	minter := NewMinter(endpoint.server.Client(), allowing(endpoint))
	credential := binding("api", profile).Credentials[0]

	for _, secret := range []string{"CANARY-first-secret-11aa", "CANARY-second-secret-22bb"} {
		if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
			t.Fatalf("write secret: %v", err)
		}
		if _, err := minter.token(t.Context(), &credential); err != nil {
			t.Fatalf("token: %v", err)
		}
		endpoint.mu.Lock()
		presented := endpoint.secret
		endpoint.mu.Unlock()
		if presented != secret {
			t.Errorf("the mint presented %q, want the secret on disk %q", presented, secret)
		}
	}
}

// A client secret that cannot be read, or reads empty, is a local failure
// naming the profile and the reference. The authorization server never sees
// a request with an empty secret in it.
func TestAnUnusableClientSecretRefusesBeforeTheNetwork(t *testing.T) {
	cases := map[string]string{
		"unset": "",
		"empty": "set-but-empty",
	}
	for name, mode := range cases {
		t.Run(name, func(t *testing.T) {
			endpoint := newTokenEndpoint(t)
			profile := endpoint.profile(t)
			profile.ClientSecretRef = config.SecretRef("env:LOTSMAN_TEST_MISSING_SECRET")
			if mode != "" {
				t.Setenv("LOTSMAN_TEST_MISSING_SECRET", "")
			}
			minter := NewMinter(endpoint.server.Client(), allowing(endpoint))
			credential := binding("api", profile).Credentials[0]

			_, err := minter.token(t.Context(), &credential)
			if err == nil {
				t.Fatal("a mint proceeded without a client secret")
			}
			if class := errs.ClassOf(err); class != errs.ClassAuth {
				t.Errorf("class = %s, want auth", class)
			}
			for _, want := range []string{`"api"`, "env:LOTSMAN_TEST_MISSING_SECRET"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the failure does not name %s: %v", want, err)
				}
			}
			if got := endpoint.mints.Load(); got != 0 {
				t.Errorf("the token endpoint was called %d times", got)
			}
		})
	}
}
