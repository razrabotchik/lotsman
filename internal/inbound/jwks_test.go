package inbound

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A key set is fetched once and then reused: a hundred calls after a
// rotation should cost one fetch, not a hundred.
func TestKeySetIsCachedForItsTTL(t *testing.T) {
	p := newProvider(t)
	clock := time.Now()
	keys := newKeySet(p.server.URL, 15*time.Minute, nil)
	keys.now = func() time.Time { return clock }

	for range 5 {
		if _, err := keys.key(t.Context(), p.kid); err != nil {
			t.Fatalf("key: %v", err)
		}
	}
	if got := p.fetches.Load(); got != 1 {
		t.Errorf("fetched %d times within the TTL, want 1", got)
	}

	clock = clock.Add(16 * time.Minute)
	if _, err := keys.key(t.Context(), p.kid); err != nil {
		t.Fatalf("key after the TTL: %v", err)
	}
	if got := p.fetches.Load(); got != 2 {
		t.Errorf("fetched %d times, want a refetch after the TTL", got)
	}
}

// A rotation is noticed without a restart, and an unknown `kid` costs at most
// one fetch per cooldown window. The bound is the point: without it a forged
// `kid` turns every request into an outbound fetch.
func TestAnUnknownKidRefetchesOncePerCooldown(t *testing.T) {
	p := newProvider(t)
	clock := time.Now()
	keys := newKeySet(p.server.URL, time.Hour, nil)
	keys.now = func() time.Time { return clock }

	if _, err := keys.key(t.Context(), p.kid); err != nil {
		t.Fatalf("key: %v", err)
	}
	fetchesAfterFirst := p.fetches.Load()

	// The issuer rotates. The cached set does not have the new key yet.
	p.kid = "key-2"
	if _, err := keys.key(t.Context(), "key-2"); err != nil {
		t.Fatalf("the rotation was not picked up: %v", err)
	}
	if got := p.fetches.Load(); got != fetchesAfterFirst+1 {
		t.Errorf("a rotation cost %d fetches, want 1", got-fetchesAfterFirst)
	}

	// A forged kid, hammered, inside the cooldown.
	before := p.fetches.Load()
	for range 20 {
		if _, err := keys.key(t.Context(), "forged"); err == nil {
			t.Fatal("a kid the issuer never published was accepted")
		}
	}
	if got := p.fetches.Load(); got != before {
		t.Errorf("a forged kid provoked %d fetches inside the cooldown, want 0", got-before)
	}

	// Past the cooldown it is allowed to ask once more.
	clock = clock.Add(2 * unknownKidCooldown)
	if _, err := keys.key(t.Context(), "forged"); err == nil {
		t.Fatal("a kid the issuer never published was accepted")
	}
	if got := p.fetches.Load(); got != before+1 {
		t.Errorf("past the cooldown the forged kid cost %d fetches, want 1", got-before)
	}
}

// An identity provider having an outage is not a reason to stop accepting
// tokens it already signed -- but it is also not a reason to accept anything
// when there is nothing to check against.
func TestAnUnreachableProviderNeitherPanicsNorOpens(t *testing.T) {
	t.Run("with keys already in force", func(t *testing.T) {
		p := newProvider(t)
		clock := time.Now()
		keys := newKeySet(p.server.URL, time.Minute, nil)
		keys.now = func() time.Time { return clock }
		if _, err := keys.key(t.Context(), p.kid); err != nil {
			t.Fatalf("key: %v", err)
		}

		p.fail.Store(true)
		clock = clock.Add(2 * time.Minute) // the TTL lapses, the refresh fails
		if _, err := keys.key(t.Context(), p.kid); err != nil {
			t.Errorf("a failed refresh dropped a key set that was working: %v", err)
		}
	})

	t.Run("with nothing cached", func(t *testing.T) {
		p := newProvider(t)
		p.fail.Store(true)
		keys := newKeySet(p.server.URL, time.Minute, nil)
		if _, err := keys.key(t.Context(), p.kid); err == nil {
			t.Error("a token was verified against a key set that could never be fetched")
		}
	})
}

// A key set document is untrusted input like any other.
func TestKeySetDocumentsAreBounded(t *testing.T) {
	cases := []struct {
		name string
		body string
		size int
	}{
		{name: "not JSON", body: "<html>sign in</html>"},
		{name: "no keys", body: `{"keys":[]}`},
		{name: "a key type this build does not implement", body: `{"keys":[{"kty":"OKP","kid":"a","crv":"Ed25519","x":"AAAA"}]}`},
		{name: "an encryption key only", body: `{"keys":[{"kty":"RSA","kid":"a","use":"enc","n":"AQAB","e":"AQAB"}]}`},
		{name: "an oversized document", size: maxJWKSBytes + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if tc.size > 0 {
				body = `{"keys":[` + strings.Repeat(" ", tc.size) + `]}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			keys := newKeySet(server.URL, time.Minute, nil)
			if _, err := keys.key(t.Context(), "a"); err == nil {
				t.Error("an unusable key set was accepted")
			}
		})
	}
}

// A provider that publishes a key this build cannot use alongside one it can
// is usable: one unreadable entry does not spoil the set.
func TestAnUnusableKeyDoesNotSpoilTheSet(t *testing.T) {
	p := newProvider(t)
	mixed := `{"keys":[{"kty":"OKP","kid":"future","crv":"Ed25519","x":"AAAA"},` +
		strings.TrimPrefix(string(p.jwks()), `{"keys":[`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mixed))
	}))
	defer server.Close()

	keys := newKeySet(server.URL, time.Minute, nil)
	if _, err := keys.key(t.Context(), p.kid); err != nil {
		t.Errorf("a usable key was lost because the set also carried an unusable one: %v", err)
	}
}
