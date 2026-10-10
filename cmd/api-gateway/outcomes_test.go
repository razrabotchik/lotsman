package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The mirror reports numbers as the literals that arrived. Decoded into
// float64 it would round 9007199254740993 itself and blame the runtime.
func TestMirrorReportsNumbersExactly(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodPost, "/v1/mirror/x",
		strings.NewReader(`{"id":9007199254740993,"limit":9223372036854775807,"ratio":0.1000000000000000055511151231257827}`),
		map[string]string{"Content-Type": "application/json"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	for _, want := range []string{`"id":9007199254740993`, `"limit":9223372036854775807`,
		`"ratio":0.1000000000000000055511151231257827`} {
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("bodyJSON does not carry %s:\n%s", want, res.Body.String())
		}
	}
}

// A null body is reflected as what it is.
func TestMirrorReportsANullBody(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodPost, "/v1/mirror/x",
		strings.NewReader(`null`), map[string]string{"Content-Type": "application/json"})
	var got mirrorReflection
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Body != "null" || got.BodyBytes != 4 {
		t.Errorf("body = %q (%d bytes), want the literal null", got.Body, got.BodyBytes)
	}
}

// The charge is made and the answer never comes: the client sees a dropped
// connection, and the list shows the charge anyway. That gap is the whole
// reason outcome_unknown exists.
func TestAChargeIsMadeAndNeverAnswered(t *testing.T) {
	server := httptest.NewServer(newTestHandler())
	defer server.Close()

	res, err := http.Post(server.URL+"/v1/charges", "application/json", strings.NewReader(`{"amount":500}`))
	if err == nil {
		_ = res.Body.Close()
		t.Fatalf("the charge was answered with %d; it must hang up", res.StatusCode)
	}

	listed, err := http.Get(server.URL + "/v1/charges")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listed.Body.Close()
	var got struct {
		Count int `json:"count"`
		Items []struct {
			Amount int `json:"amount"`
		} `json:"items"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Count != 1 || got.Items[0].Amount != 500 {
		t.Errorf("charges = %+v, want the one that was never answered", got)
	}
}

// A charge the fixture cannot read is refused with an answer: only a charge
// that was made is hung up on.
func TestAnUnreadableChargeIsAnswered(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodPost, "/v1/charges",
		strings.NewReader(`{"amount":0}`), map[string]string{"Content-Type": "application/json"})
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
}

// The status arrives and the body stops halfway.
func TestTruncatedSendsAStatusAndHalfABody(t *testing.T) {
	server := httptest.NewServer(newTestHandler())
	defer server.Close()

	res, err := http.Get(server.URL + "/v1/shapes/truncated")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("reading the body ended with %v, want an unexpected EOF", err)
	}
	if len(body) == 0 || int64(len(body)) >= res.ContentLength {
		t.Errorf("read %d of %d promised bytes, want part of them", len(body), res.ContentLength)
	}
}

func TestPingIsTheAPIs(t *testing.T) {
	res := request(t, newTestHandler(), http.MethodGet, "/v1/ping", nil, nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"pong":true`)) {
		t.Errorf("ping = %d %s", res.Code, res.Body.String())
	}
}

// Both secrets of a rotation are accepted, and stats names which one the last
// mint presented -- never its value.
func TestARotatedSecretIsAcceptedAndNamed(t *testing.T) {
	h := newTestHandler()
	for _, tc := range []struct{ secret, name string }{
		{oauthClientSecret, "current"}, {oauthNextClientSecret, "next"},
	} {
		minted := mint(t, h, tc.secret)
		if minted == "" {
			t.Fatalf("the %s secret was refused", tc.name)
		}
		stats := request(t, h, http.MethodGet, "/oauth/stats", nil, nil).Body.String()
		if !strings.Contains(stats, `"lastSecret":"`+tc.name+`"`) {
			t.Errorf("after a mint with the %s secret, stats = %s", tc.name, stats)
		}
		if strings.Contains(stats, tc.secret) {
			t.Errorf("stats printed the secret itself: %s", stats)
		}
	}
}

// Revoking makes every issued token a 401 at once; a fresh mint works.
func TestRevokeStalesEveryIssuedToken(t *testing.T) {
	h := newTestHandler()
	tokens := []string{mint(t, h, oauthClientSecret), mint(t, h, oauthClientSecret)}

	revoked := request(t, h, http.MethodPost, "/oauth/revoke", nil, nil)
	if !strings.Contains(revoked.Body.String(), `"revoked":2`) {
		t.Errorf("revoke = %s", revoked.Body.String())
	}
	for _, token := range tokens {
		if got := request(t, h, http.MethodGet, "/v1/minted/profile", nil,
			map[string]string{"Authorization": "Bearer " + token}); got.Code != http.StatusUnauthorized {
			t.Errorf("a revoked token was accepted: %d", got.Code)
		}
	}
	fresh := mint(t, h, oauthClientSecret)
	if got := request(t, h, http.MethodGet, "/v1/minted/profile", nil,
		map[string]string{"Authorization": "Bearer " + fresh}); got.Code != http.StatusOK {
		t.Errorf("a token minted after the revocation was refused: %d", got.Code)
	}
}

// mint obtains a token with the given secret, or "" when it is refused.
func mint(t *testing.T, h http.Handler, secret string) string {
	t.Helper()
	res := request(t, h, http.MethodPost, "/oauth/token",
		strings.NewReader("grant_type=client_credentials&client_id="+oauthClientID+"&client_secret="+secret),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if res.Code != http.StatusOK {
		return ""
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	return issued.AccessToken
}
