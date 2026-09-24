package inbound

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
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

// A key set may not hand this process a key that is expensive to verify
// against, or cheap to forge against.
//
// The expensive direction is the one that is easy to miss: verifying an RS256
// signature against a 4-Mbit modulus takes about 43 seconds on the machine
// these numbers were measured on, against 36 µs for a 2048-bit one — and the
// verification runs before a caller is authenticated, so one unauthenticated
// request is enough to spend it.
func TestRSAKeysOutsideTheAcceptedSizesAreRefused(t *testing.T) {
	cases := []struct {
		name string
		bits int
	}{
		{name: "small enough to factor", bits: 512},
		{name: "just under the floor", bits: minRSABits - 8},
		{name: "just over the ceiling", bits: maxRSABits + 8},
		{name: "expensive enough to be a weapon", bits: 1 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modulus := new(big.Int).Lsh(big.NewInt(1), uint(tc.bits-1))
			modulus.SetBit(modulus, 0, 1)
			document := jwksWith(map[string]string{
				"kty": "RSA", "kid": "a",
				"n": base64.RawURLEncoding.EncodeToString(modulus.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes()),
			})
			if _, err := parseJWKS(document); err == nil {
				t.Errorf("a %d-bit RSA key was accepted", tc.bits)
			}
		})
	}
}

// And the exponent, which costs the same exponentiation the modulus does --
// and where 1 would make every signature verify.
func TestUnusableRSAExponentsAreRefused(t *testing.T) {
	modulus := new(big.Int).Lsh(big.NewInt(1), minRSABits-1)
	modulus.SetBit(modulus, 0, 1)

	for _, exponent := range []*big.Int{
		big.NewInt(1),
		big.NewInt(0),
		big.NewInt(65536),                   // even
		new(big.Int).Lsh(big.NewInt(1), 40), // larger than anything real
	} {
		document := jwksWith(map[string]string{
			"kty": "RSA", "kid": "a",
			"n": base64.RawURLEncoding.EncodeToString(modulus.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(exponent.Bytes()),
		})
		if _, err := parseJWKS(document); err == nil {
			t.Errorf("exponent %s was accepted", exponent)
		}
	}
}

// A key of an ordinary size still works, so the bounds above refuse the right
// things rather than everything.
func TestAnOrdinaryRSAKeyIsStillAccepted(t *testing.T) {
	p := newProvider(t)
	keys, err := parseJWKS(p.jwks())
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}
	if _, ok := keys[p.kid]; !ok {
		t.Error("a 2048-bit RSA key was refused")
	}
}

// jwksWith renders a one-key document.
func jwksWith(key map[string]string) []byte {
	document, err := json.Marshal(map[string]any{"keys": []map[string]string{key}})
	if err != nil {
		panic(err)
	}
	return document
}
