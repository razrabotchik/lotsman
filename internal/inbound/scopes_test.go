package inbound

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/razrabotchik/lotsman/internal/config"
)

// Scopes decide what an authenticated caller may do, and providers disagree
// about how to spell them: RFC 8693's space-delimited `scope` string, the `scp`
// array Entra emits, and the `scp` string some others emit instead.
//
// The `scp`-as-a-string shape had no test. It fails safe -- no scopes means any
// call requiring one is refused -- but "safe" here means every caller from that
// provider is turned away, which is an outage rather than a defence.
func TestScopesAreReadFromEveryShapeInTheWild(t *testing.T) {
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   []string
	}{
		{
			name:   "the space-delimited scope string",
			claims: jwt.MapClaims{"scope": "things:read things:write"},
			want:   []string{"things:read", "things:write"},
		},
		{
			name:   "an scp array",
			claims: jwt.MapClaims{"scp": []any{"things:read", "things:write"}},
			want:   []string{"things:read", "things:write"},
		},
		{
			name:   "an scp string",
			claims: jwt.MapClaims{"scp": "things:read things:write"},
			want:   []string{"things:read", "things:write"},
		},
		{
			name:   "scope wins when a token carries both",
			claims: jwt.MapClaims{"scope": "from-scope", "scp": []any{"from-scp"}},
			want:   []string{"from-scope"},
		},
		{
			name:   "an scp array with entries that are not strings",
			claims: jwt.MapClaims{"scp": []any{"things:read", 7, nil, "things:write"}},
			want:   []string{"things:read", "things:write"},
		},
		{
			name:   "no scopes at all",
			claims: jwt.MapClaims{"sub": "someone"},
			want:   nil,
		},
		{
			// A shape nobody emits yields nothing rather than a guess: a call
			// that required a scope is then refused, which is the safe
			// direction for a claim this code did not understand.
			name:   "a shape nobody emits",
			claims: jwt.MapClaims{"scp": map[string]any{"things": "read"}},
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scopesFrom(tc.claims)
			if !slices.Equal(got, tc.want) {
				t.Errorf("scopes = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// The JWKS lifetime is the operator's, and a value they wrote has to be the one
// in force -- or refused by name. Neither branch was exercised: the validator
// refuses a malformed duration at load time, so this one is the second line of
// defence for a configuration assembled in process.
func TestTheConfiguredJWKSLifetimeIsHonouredOrNamed(t *testing.T) {
	base := &config.InboundOAuth{
		Issuer:   "https://as.example.com",
		Resource: "https://lotsman.example.com",
		JWKSURI:  "https://as.example.com/jwks",
	}
	log := slog.New(slog.DiscardHandler)

	withTTL := *base
	withTTL.JWKSTTL = "45s"
	if _, err := oauthTokenVerifier(&withTTL, log, http.DefaultClient); err != nil {
		t.Errorf("a configured jwksTTL was refused: %v", err)
	}

	broken := *base
	broken.JWKSTTL = "eventually"
	_, err := oauthTokenVerifier(&broken, log, http.DefaultClient)
	if err == nil {
		t.Fatal("a jwksTTL that is not a duration was accepted")
	}
	if !strings.Contains(err.Error(), "jwksTTL") {
		t.Errorf("the refusal does not name the setting: %v", err)
	}
}
