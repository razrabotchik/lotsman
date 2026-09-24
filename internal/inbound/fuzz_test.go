package inbound

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzParseJWKS pins what a key set document may do to this process.
//
// It is network input from an authorization server, parsed into cryptographic
// material with big-integer arithmetic and a curve check. The properties are:
// it never panics on anything, and anything it hands back is a key of a type
// this build can actually verify with -- a half-built key returned from here
// would fail much later, inside a verification, as something that looks like
// a bad token rather than a bad key set.
func FuzzParseJWKS(f *testing.F) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, seed := range []string{
		`{"keys":[]}`,
		`{"keys":[{"kty":"RSA","kid":"a","n":"` + encode("modulus") + `","e":"AQAB"}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","use":"enc","n":"` + encode("m") + `","e":"AQAB"}]}`,
		`{"keys":[{"kty":"EC","kid":"a","crv":"P-256","x":"","y":""}]}`,
		`{"keys":[{"kty":"EC","kid":"a","crv":"P-384","x":"AAAA","y":"AAAA"}]}`,
		`{"keys":[{"kty":"OKP","kid":"a","crv":"Ed25519","x":"AAAA"}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","n":"!!!not base64!!!","e":"AQAB"}]}`,
		`{"keys":[{"kty":"RSA","kid":"","n":"` + encode("m") + `","e":"` + encode("\x00") + `"}]}`,
		`{"keys":null}`,
		`{}`,
		`[]`,
		`not json at all`,
		"",
		`{"keys":[{"kty":"RSA","kid":"a","n":"` + strings.Repeat("A", 4096) + `","e":"AQAB"}]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, document []byte) {
		keys, err := parseJWKS(document)
		if err != nil {
			if keys != nil {
				t.Fatalf("parseJWKS returned %d keys alongside an error", len(keys))
			}
			return
		}
		if len(keys) == 0 {
			t.Fatal("parseJWKS reported success with no keys; an empty set must be an error")
		}
		for kid, key := range keys {
			switch typed := key.(type) {
			case *rsa.PublicKey:
				if typed == nil || typed.N == nil || typed.E <= 0 {
					t.Fatalf("key %q is an unusable RSA key: %+v", kid, typed)
				}
			case *ecdsa.PublicKey:
				if typed == nil || typed.Curve == nil || typed.X == nil || typed.Y == nil {
					t.Fatalf("key %q is an unusable EC key: %+v", kid, typed)
				}
				// ParseUncompressedPublicKey is supposed to have refused a
				// point that is not on the curve. If one ever gets here,
				// every signature verified against it is meaningless.
				if !typed.Curve.IsOnCurve(typed.X, typed.Y) { //nolint:staticcheck // SA1019: asserting the invariant the parser is supposed to hold.
					t.Fatalf("key %q is not on its own curve", kid)
				}
			default:
				t.Fatalf("key %q has type %T, which nothing in this build can verify with", kid, key)
			}
		}
	})
}

// FuzzAudienceContains pins the predicate that decides whether a token
// introspected by the authorization server was minted for *this* resource.
//
// The claim is arbitrary JSON: a string, a list, a number, a nested object,
// or absent. Every shape that is not an exact match must be a refusal --
// reading anything else as "for me" would accept every token the server ever
// issued, for any of its resources.
func FuzzAudienceContains(f *testing.F) {
	for _, seed := range []struct{ claimed, want string }{
		{`"https://mcp.example.com/"`, "https://mcp.example.com/"},
		{`["https://other/","https://mcp.example.com/"]`, "https://mcp.example.com/"},
		{`null`, "https://mcp.example.com/"},
		{`[]`, "https://mcp.example.com/"},
		{`123`, "https://mcp.example.com/"},
		{`{"aud":"https://mcp.example.com/"}`, "https://mcp.example.com/"},
		{`""`, ""},
		{`"https://mcp.example.com"`, "https://mcp.example.com/"},
	} {
		f.Add(seed.claimed, seed.want)
	}

	f.Fuzz(func(t *testing.T, claimedJSON, want string) {
		claimed := decodeAny(claimedJSON)
		if !audienceContains(claimed, want) {
			return
		}
		// It matched. The only ways that may happen are an exact string or a
		// list carrying one.
		switch value := claimed.(type) {
		case string:
			if value != want {
				t.Fatalf("a string audience %q matched a want of %q", value, want)
			}
		case []any:
			for _, entry := range value {
				if text, ok := entry.(string); ok && text == want {
					return
				}
			}
			t.Fatalf("a list audience %v matched a want of %q that it does not contain", value, want)
		default:
			t.Fatalf("an audience of type %T matched; only a string or a list of them may", claimed)
		}
	})
}

// FuzzStaticBearer pins the comparison: the configured token is accepted and
// nothing else is, whatever a caller presents.
func FuzzStaticBearer(f *testing.F) {
	const configured = "the-configured-token"
	for _, seed := range []string{
		configured,
		"",
		configured + " ",
		" " + configured,
		strings.ToUpper(configured),
		configured[:5],
		strings.Repeat(configured, 100),
		"\x00" + configured,
	} {
		f.Add(seed)
	}

	verify := staticBearer(configured)
	f.Fuzz(func(t *testing.T, presented string) {
		info, err := verify(t.Context(), presented, nil)
		if presented == configured {
			if err != nil || info == nil {
				t.Fatalf("the configured token was refused: %v", err)
			}
			return
		}
		if err == nil {
			t.Fatalf("a token that is not the configured one was accepted: %q", presented)
		}
		if info != nil {
			t.Fatal("a refusal returned token info")
		}
	})
}

// decodeAny is the shape an introspection response arrives in.
func decodeAny(document string) any {
	var value any
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		return nil
	}
	return value
}
