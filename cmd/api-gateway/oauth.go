package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The client-credentials flow, as an upstream sees it. This is what makes M4a
// testable by hand: lotsman mints a token from a client id and secret, caches
// it per profile, and on a 401 re-mints exactly once and retries once (FR-35).
//
// Three endpoints, each answering a different question:
//
//   - POST /oauth/token            -- does lotsman ask correctly, and for what?
//   - GET  /v1/minted/profile      -- does it present what it was given?
//   - GET  /v1/minted/stale-once   -- does a 401 produce a re-mint and a retry
//                                     that succeeds?
//   - GET  /v1/minted/always-stale -- and does it then stop? One retry, not a
//                                     loop.
//
// The two refusing endpoints are the two halves of FR-35, and the difference
// between them is the whole lesson. stale-once refuses the first call it ever
// receives and accepts everything after: a runtime that re-mints and retries
// sees a 200. always-stale refuses every token the first time it sees that
// token, so a re-mint cannot help -- the second 401 is the API's answer and
// belongs to the caller.
//
// Neither is observable without the counters: /oauth/stats reports how many
// tokens were minted, which is what tells "re-minted and retried" apart from
// "retried the same token".

const (
	oauthClientID = "demo-client"
	// A fixture's credential, published in its own README on purpose: the point
	// is that an operator can run the client-credentials flow without owning an
	// authorization server. It authenticates nothing but this process.
	oauthClientSecret = "demo-client-secret" //nolint:gosec // G101: a development fixture's documented demo credential, not a secret.
	// tokenLifetime is short enough to watch an expiry happen and long enough
	// that a session's calls share one token.
	tokenLifetime = 120 * time.Second
)

// tokens is the authorization server's memory.
type tokens struct {
	mu sync.Mutex
	// issued maps a token to when it expires.
	issued map[string]time.Time
	// refused records the tokens always-stale has already turned down, so each
	// one is refused at most once.
	refused map[string]bool
	// firstCallSeen is how stale-once knows it has already refused somebody.
	firstCallSeen bool
	// mints counts how many tokens were issued: the number a test asserts on
	// when it wants to know that a retry re-minted rather than repeated.
	mints int
	// scopes records what the last mint asked for.
	lastScopes string
	// style records how the last mint authenticated itself: "basic" when the
	// client id and secret arrived in the Authorization header, "body" when
	// they arrived as form fields.
	lastStyle string
}

func newTokens() *tokens {
	return &tokens{issued: map[string]time.Time{}, refused: map[string]bool{}}
}

func (t *tokens) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /oauth/token", t.mint)
	mux.HandleFunc("GET /oauth/stats", t.stats)
	mux.HandleFunc("GET /v1/minted/profile", t.profile)
	mux.HandleFunc("GET /v1/minted/stale-once", t.staleOnce)
	mux.HandleFunc("GET /v1/minted/always-stale", t.alwaysStale)
}

// mint is the token endpoint (RFC 6749 §4.4).
func (t *tokens) mint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "form body expected")
		return
	}
	if got := r.PostForm.Get("grant_type"); got != "client_credentials" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"this fixture implements client_credentials only, not "+quoted(got))
		return
	}

	style := "body"
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if headerID, headerSecret, ok := r.BasicAuth(); ok {
		style, id, secret = "basic", headerID, headerSecret
	}
	if id != oauthClientID || secret != oauthClientSecret {
		// 401 with invalid_client is what the RFC asks for, and it is also the
		// status a client must not retry on: the credentials are wrong, not
		// stale.
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client id or secret is wrong")
		return
	}

	token := randomToken()
	t.mu.Lock()
	t.issued[token] = time.Now().Add(tokenLifetime)
	t.mints++
	t.lastScopes = r.PostForm.Get("scope")
	t.lastStyle = style
	t.mu.Unlock()

	// No caching, ever: a cached token response is a token two callers share.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(tokenLifetime.Seconds()),
		"scope":        r.PostForm.Get("scope"),
	})
}

// stats reports what the authorization server has been asked for. It is the
// endpoint a person uses to check that one profile minted one token for twenty
// calls, and that a 401 produced exactly one more.
func (t *tokens) stats(w http.ResponseWriter, _ *http.Request) {
	t.mu.Lock()
	defer t.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"mints":       t.mints,
		"live":        len(t.issued),
		"lastScopes":  t.lastScopes,
		"lastStyle":   t.lastStyle,
		"refusedOnce": len(t.refused),
	})
}

// profile needs a minted token and says which one it saw -- truncated, because
// a fixture that prints a whole credential teaches the wrong habit.
func (t *tokens) profile(w http.ResponseWriter, r *http.Request) {
	token, ok := t.accept(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject":    "service-account",
		"tokenShown": fingerprint(token),
	})
}

// staleOnce refuses the first call it ever receives and accepts the rest. That
// is the ordinary shape of an expired token, and the one where FR-35's re-mint
// and retry produces a working call rather than a second refusal.
func (t *tokens) staleOnce(w http.ResponseWriter, r *http.Request) {
	token, ok := t.accept(w, r)
	if !ok {
		return
	}
	t.mu.Lock()
	first := !t.firstCallSeen
	t.firstCallSeen = true
	t.mu.Unlock()

	if first {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token",
			"the first call here is always refused; retry with a fresh token and it will work")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":   true,
		"tokenShown": fingerprint(token),
	})
}

// alwaysStale refuses every token the first time it sees that token, so no
// re-mint can satisfy it: the retry happens, fails, and stops. A runtime that
// looped here would mint for ever, and the mint counter is where that would
// show.
func (t *tokens) alwaysStale(w http.ResponseWriter, r *http.Request) {
	token, ok := t.accept(w, r)
	if !ok {
		return
	}
	t.mu.Lock()
	first := !t.refused[token]
	t.refused[token] = true
	t.mu.Unlock()

	if first {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token",
			"this token is new to this endpoint, and every new token is refused once")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":   true,
		"tokenShown": fingerprint(token),
	})
}

// accept checks the bearer token and reports it, answering 401 itself when
// there is nothing usable to report.
func (t *tokens) accept(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "a minted bearer token is required")
		return "", false
	}
	t.mu.Lock()
	expiry, known := t.issued[token]
	t.mu.Unlock()
	switch {
	case !known:
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "this token was not issued here")
		return "", false
	case time.Now().After(expiry):
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "this token has expired")
		return "", false
	}
	return token, true
}

func randomToken() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand does not fail in practice, and a fixture that silently
		// issued a predictable token would be worse than one that stops.
		panic("api-gateway: cannot read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// fingerprint is enough of a token to tell two apart and not enough to use.
func fingerprint(token string) string {
	if len(token) <= 8 {
		return "…"
	}
	return token[:8] + "…"
}

func quoted(value string) string {
	if value == "" {
		return "nothing"
	}
	return `"` + value + `"`
}

func writeOAuthError(w http.ResponseWriter, statusCode int, code, description string) {
	writeJSON(w, statusCode, map[string]any{
		"error":             code,
		"error_description": description,
	})
}
