package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The fixture's own tests answer one question: does it still produce the shape
// it advertises? Every endpoint here exists so that something in lotsman can be
// observed, and an endpoint that quietly stopped producing its shape would make
// the observation meaningless rather than failing.

// The mirror is only useful if it reports what arrived without interpreting it.
func TestMirrorReportsWhatArrived(t *testing.T) {
	h := newTestHandler()
	// A path segment carrying an encoded separator: the case where "did this
	// stay one segment" is the whole question.
	res := request(t, h, http.MethodGet, "/v1/mirror/a%2Fb?tag=x&tag=y&csv=p,q", nil,
		map[string]string{"X-Trace": "t-1", "Authorization": "Bearer should-not-be-shown"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}

	var got mirrorReflection
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Segments) != 3 {
		t.Errorf("segments = %v, want three: the encoded separator stayed inside one", got.Segments)
	}
	if want := []string{"x", "y"}; len(got.Query["tag"]) != 2 || got.Query["tag"][0] != want[0] {
		t.Errorf("query[tag] = %v, want %v: repeats must survive", got.Query["tag"], want)
	}
	if got.Query["csv"][0] != "p,q" {
		t.Errorf("query[csv] = %v, want the unexploded value intact", got.Query["csv"])
	}
	if got.Headers["X-Trace"][0] != "t-1" {
		t.Errorf("X-Trace = %v", got.Headers["X-Trace"])
	}
	// A credential is reported as present and never as itself.
	if shown := got.Headers["Authorization"]; len(shown) != 1 || strings.Contains(shown[0], "should-not-be-shown") {
		t.Errorf("Authorization = %v, want it named and not valued", shown)
	}
}

func TestMirrorReportsABody(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodPost, "/v1/mirror/x",
		strings.NewReader(`{"name":"Мурка","tags":["кот"]}`),
		map[string]string{"Content-Type": "application/json"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	var got mirrorReflection
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded, ok := got.BodyJSON.(map[string]any)
	if !ok {
		t.Fatalf("bodyJSON = %#v, want the decoded object", got.BodyJSON)
	}
	if decoded["name"] != "Мурка" {
		t.Errorf("bodyJSON[name] = %v", decoded["name"])
	}
	if got.BodyBytes == 0 {
		t.Error("bodyBytes = 0 for a body that was sent")
	}
}

// The client-credentials flow, end to end against the fixture: a mint, a call
// that presents the token, and the 401-once endpoint that FR-35's single retry
// exists for.
func TestMintedTokenFlow(t *testing.T) {
	h := newTestHandler()

	refused := request(t, h, http.MethodPost, "/oauth/token",
		strings.NewReader("grant_type=client_credentials&client_id=demo-client&client_secret=wrong"),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if refused.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong secret minted a token: %d %s", refused.Code, refused.Body.String())
	}

	minted := request(t, h, http.MethodPost, "/oauth/token",
		strings.NewReader("grant_type=client_credentials&client_id="+oauthClientID+
			"&client_secret="+oauthClientSecret+"&scope=pets%3Aread"),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if minted.Code != http.StatusOK {
		t.Fatalf("mint failed: %d %s", minted.Code, minted.Body.String())
	}
	var issued struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if issued.AccessToken == "" || issued.TokenType != "Bearer" || issued.ExpiresIn <= 0 {
		t.Fatalf("token response = %+v", issued)
	}
	if issued.Scope != "pets:read" {
		t.Errorf("scope = %q, want what was asked for", issued.Scope)
	}

	bearer := map[string]string{"Authorization": "Bearer " + issued.AccessToken}
	if got := request(t, h, http.MethodGet, "/v1/minted/profile", nil, bearer); got.Code != http.StatusOK {
		t.Fatalf("the minted token was refused: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, h, http.MethodGet, "/v1/minted/profile", nil,
		map[string]string{"Authorization": "Bearer invented"}); got.Code != http.StatusUnauthorized {
		t.Errorf("an invented token was accepted: %d", got.Code)
	}

	// stale-once refuses the first call it ever sees and accepts the rest, so a
	// retry -- with a fresh token or the same one -- works.
	first := request(t, h, http.MethodGet, "/v1/minted/stale-once", nil, bearer)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("stale-once did not refuse the first call: %d", first.Code)
	}
	second := request(t, h, http.MethodGet, "/v1/minted/stale-once", nil, bearer)
	if second.Code != http.StatusOK {
		t.Fatalf("stale-once refused the second call too: %d %s", second.Code, second.Body.String())
	}

	// always-stale refuses each *token* once, so repeating the same token works
	// and a fresh one is refused again. That is what makes a single retry
	// visible as a single retry rather than as a loop.
	if got := request(t, h, http.MethodGet, "/v1/minted/always-stale", nil, bearer); got.Code != http.StatusUnauthorized {
		t.Fatalf("always-stale accepted a token it had not seen: %d", got.Code)
	}
	if got := request(t, h, http.MethodGet, "/v1/minted/always-stale", nil, bearer); got.Code != http.StatusOK {
		t.Fatalf("always-stale refused a token twice: %d %s", got.Code, got.Body.String())
	}

	stats := request(t, h, http.MethodGet, "/oauth/stats", nil, nil)
	var counters struct {
		Mints      int    `json:"mints"`
		LastScopes string `json:"lastScopes"`
		LastStyle  string `json:"lastStyle"`
	}
	if err := json.Unmarshal(stats.Body.Bytes(), &counters); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if counters.Mints != 1 {
		t.Errorf("mints = %d, want 1: only one token was ever asked for", counters.Mints)
	}
	if counters.LastStyle != "body" {
		t.Errorf("lastStyle = %q, want body for form-field credentials", counters.LastStyle)
	}
}

// The token endpoint accepts HTTP basic as well, which is the other half of
// `authStyle` in a profile.
func TestMintWithBasicAuthentication(t *testing.T) {
	h := newTestHandler()
	// Encoded here rather than pasted, so the header cannot drift from the
	// credentials the fixture actually accepts.
	credentials := base64.StdEncoding.EncodeToString([]byte(oauthClientID + ":" + oauthClientSecret))
	req := request(t, h, http.MethodPost, "/oauth/token",
		strings.NewReader("grant_type=client_credentials"),
		map[string]string{
			"Content-Type":  "application/x-www-form-urlencoded",
			"Authorization": "Basic " + credentials,
		})
	if req.Code != http.StatusOK {
		t.Fatalf("basic authentication was refused: %d %s", req.Code, req.Body.String())
	}
	stats := request(t, h, http.MethodGet, "/oauth/stats", nil, nil)
	if !strings.Contains(stats.Body.String(), `"lastStyle":"basic"`) {
		t.Errorf("lastStyle was not recorded as basic: %s", stats.Body.String())
	}
}

// Each response shape is here because something downstream has to decide what
// to do with it; a shape that changed silently would move the decision without
// moving the test that covers it.
func TestResponseShapes(t *testing.T) {
	h := newTestHandler()
	tests := []struct {
		name        string
		path        string
		status      int
		contentType string
		check       func(t *testing.T, header http.Header, body string)
	}{
		{
			name: "204 carries no body", path: "/v1/shapes/no-content",
			status: http.StatusNoContent,
			check: func(t *testing.T, _ http.Header, body string) {
				if body != "" {
					t.Errorf("body = %q, want empty", body)
				}
			},
		},
		{
			name: "text is text", path: "/v1/shapes/text",
			status: http.StatusOK, contentType: "text/plain; charset=utf-8",
		},
		{
			name: "binary is not text", path: "/v1/shapes/binary",
			status: http.StatusOK, contentType: "application/octet-stream",
			check: func(t *testing.T, _ http.Header, body string) {
				if !strings.ContainsRune(body, 0) {
					t.Error("the body carries no NUL, so it is not the shape this is for")
				}
			},
		},
		{
			name: "the credential headers are present to be dropped", path: "/v1/shapes/cookie",
			status: http.StatusOK,
			check: func(t *testing.T, header http.Header, _ string) {
				if header.Get("Set-Cookie") == "" || header.Get("Authorization") == "" {
					t.Error("the fixture must answer with both headers for the drop to be observable")
				}
			},
		},
		{
			name: "rate-limit headers travel with a 429", path: "/v1/shapes/rate-limited",
			status: http.StatusTooManyRequests,
			check: func(t *testing.T, header http.Header, _ string) {
				if header.Get("RateLimit-Remaining") != "0" {
					t.Errorf("RateLimit-Remaining = %q", header.Get("RateLimit-Remaining"))
				}
			},
		},
		{
			name: "a problem document is a problem document", path: "/v1/shapes/problem",
			status: http.StatusUnprocessableEntity, contentType: "application/problem+json",
		},
		{
			name: "many headers, one long", path: "/v1/shapes/headers?count=3&length=10",
			status: http.StatusOK,
			check: func(t *testing.T, header http.Header, _ string) {
				if header.Get("X-Fixture-2") == "" {
					t.Error("the numbered headers are missing")
				}
				if len(header.Get("X-Request-Id")) != 10 {
					t.Errorf("X-Request-Id length = %d, want 10", len(header.Get("X-Request-Id")))
				}
			},
		},
		{
			name: "a redirect chain counts down", path: "/v1/redirect/chain?hops=1",
			status: http.StatusFound,
			check: func(t *testing.T, header http.Header, _ string) {
				if got := header.Get("Location"); got != "/v1/redirect/chain?hops=0" {
					t.Errorf("Location = %q", got)
				}
			},
		},
		{
			name: "a redirect to another origin says so", path: "/v1/redirect/external",
			status: http.StatusFound,
			check: func(t *testing.T, header http.Header, _ string) {
				if !strings.HasPrefix(header.Get("Location"), "https://example.com/") {
					t.Errorf("Location = %q, want another origin", header.Get("Location"))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := request(t, h, http.MethodGet, tt.path, nil, nil)
			if res.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", res.Code, tt.status, res.Body.String())
			}
			if tt.contentType != "" && res.Header().Get("Content-Type") != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", res.Header().Get("Content-Type"), tt.contentType)
			}
			if tt.check != nil {
				tt.check(t, res.Header(), res.Body.String())
			}
		})
	}
}

// The gzip endpoint has to produce a body that is both compressed and valid
// JSON once decompressed, or it measures nothing.
func TestGzippedBodyDecompressesToJSON(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodGet, "/v1/shapes/gzip?bytes=128", nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if got := res.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	reader, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("the body is not gzip: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var document struct {
		Filler string `json:"filler"`
	}
	if err := json.Unmarshal(decompressed, &document); err != nil {
		t.Fatalf("decompressed body is not JSON: %v", err)
	}
	if len(document.Filler) != 128 {
		t.Errorf("filler length = %d, want 128", len(document.Filler))
	}
}

// The write verbs, since they are what effect classification is read from.
func TestWriteVerbs(t *testing.T) {
	h := newTestHandler()
	key := map[string]string{"X-API-Key": apiKey}

	replaced := request(t, h, http.MethodPut, "/v1/pets/1",
		strings.NewReader(`{"name":"Replaced","tags":["new"]}`), key)
	if replaced.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", replaced.Code, replaced.Body.String())
	}

	wrongType := request(t, h, http.MethodPatch, "/v1/pets/1",
		strings.NewReader(`{"name":"X"}`), map[string]string{
			"X-API-Key":    apiKey,
			"Content-Type": "application/json",
		})
	if wrongType.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a patch with the wrong media type was accepted: %d", wrongType.Code)
	}

	patched := request(t, h, http.MethodPatch, "/v1/pets/1",
		strings.NewReader(`{"tags":["patched"]}`), map[string]string{
			"X-API-Key":    apiKey,
			"Content-Type": "application/merge-patch+json",
		})
	if patched.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d: %s", patched.Code, patched.Body.String())
	}
	// An absent field is left alone; that is what distinguishes a merge patch
	// from a replacement.
	if !strings.Contains(patched.Body.String(), `"name":"Replaced"`) {
		t.Errorf("the patch overwrote a field it did not mention: %s", patched.Body.String())
	}

	renamed := request(t, h, http.MethodPost, "/v1/pets/1/actions/rename",
		strings.NewReader(`{"name":"Renamed"}`), key)
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", renamed.Code, renamed.Body.String())
	}
}

// The deliberately-refused endpoints answer, so that the day one of them stops
// being refused there is something behind it.
func TestRefusedEndpointsStillWork(t *testing.T) {
	h := newTestHandler()

	form := request(t, h, http.MethodPost, "/v1/forms/urlencoded",
		strings.NewReader("name=Rex&tags=dog"),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if form.Code != http.StatusOK || !strings.Contains(form.Body.String(), "Rex") {
		t.Errorf("form: %d %s", form.Code, form.Body.String())
	}

	filtered := request(t, h, http.MethodGet,
		"/v1/filters?filter%5Bname%5D=Murka&tags=a+b&ids=1%7C2", nil, nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("filters: %d %s", filtered.Code, filtered.Body.String())
	}
	for _, want := range []string{`"name":"Murka"`, `"a"`, `"b"`, `"1"`, `"2"`} {
		if !strings.Contains(filtered.Body.String(), want) {
			t.Errorf("filters body does not contain %s: %s", want, filtered.Body.String())
		}
	}

	competing := request(t, h, http.MethodPatch, "/v1/pets/1/competing",
		strings.NewReader(`{}`), map[string]string{
			"X-API-Key":    apiKey,
			"Content-Type": "application/strategic-merge-patch+json",
		})
	if competing.Code != http.StatusOK {
		t.Errorf("competing patch: %d %s", competing.Code, competing.Body.String())
	}

	conditional := request(t, h, http.MethodPost, "/v1/conditional",
		strings.NewReader(`{"kind":"product","sku":"A-1"}`),
		map[string]string{"Content-Type": "application/json"})
	if conditional.Code != http.StatusOK || !strings.Contains(conditional.Body.String(), "product") {
		t.Errorf("conditional: %d %s", conditional.Code, conditional.Body.String())
	}
}

// The certificate has to be one a client can verify, or the token endpoint is
// unreachable for the only reason it exists.
func TestGeneratedCertificateIsUsable(t *testing.T) {
	cert, err := newCertificate(t.TempDir())
	if err != nil {
		t.Fatalf("newCertificate: %v", err)
	}
	if len(cert.tls.Certificate) == 0 {
		t.Fatal("the key pair carries no certificate")
	}
	if !strings.HasPrefix(string(cert.pem), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("the PEM is not a certificate: %.40s", cert.pem)
	}
	if strings.Contains(string(cert.pem), "PRIVATE KEY") {
		t.Error("the private key is in the file a client is told to trust")
	}
}

// The certificate is reused across restarts, because regenerating it broke the
// trust a client had already been given -- and the mint then failed with a
// certificate error that says nothing about what changed.
func TestCertificateIsReusedAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	first, err := newCertificate(dir)
	if err != nil {
		t.Fatalf("newCertificate: %v", err)
	}
	second, err := newCertificate(dir)
	if err != nil {
		t.Fatalf("newCertificate again: %v", err)
	}
	if !bytes.Equal(first.pem, second.pem) {
		t.Error("a second start issued a different certificate; every client would have to be restarted")
	}

	// A certificate that is gone is issued again rather than failing.
	if removeErr := os.Remove(first.path); removeErr != nil {
		t.Fatal(removeErr)
	}
	third, err := newCertificate(dir)
	if err != nil {
		t.Fatalf("newCertificate after removal: %v", err)
	}
	if bytes.Equal(third.pem, first.pem) {
		t.Error("the certificate was not reissued after being removed")
	}
}
