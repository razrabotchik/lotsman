package inbound

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// maxJWKSBytes bounds what a key set may be. The document comes from an
// authorization server over the network, which makes it untrusted input like
// any other: a provider having a bad day must not be able to exhaust this
// process.
const maxJWKSBytes = 1 << 20

// maxJWKSKeys bounds how many keys one set may carry.
const maxJWKSKeys = 64

// unknownKidCooldown is the shortest interval between two fetches provoked by
// a `kid` the cached set does not contain.
//
// Without it, a token carrying a `kid` nobody has ever issued turns every
// request into an outbound fetch, and the identity provider finds out about
// it before we do. With it, a genuine rotation costs one extra fetch and an
// attacker gets one per window however hard they try.
const unknownKidCooldown = time.Minute

// keySet is an issuer's public keys, cached.
//
// The cache is a decision about how long a revoked key keeps working, which
// is why the TTL is configuration with a documented default rather than a
// number chosen here.
type keySet struct {
	uri    string
	ttl    time.Duration
	client *http.Client
	now    func() time.Time

	// One mutex over the whole fetch-and-read cycle. It serializes concurrent
	// requests behind a single fetch, which is the behaviour worth having: a
	// hundred calls arriving after a rotation should cost one fetch, not a
	// hundred.
	mu        sync.Mutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
	// lastUnknownKid is when a fetch was last *provoked by* a kid the set did
	// not contain. It is deliberately not the same clock as an ordinary TTL
	// refresh: a routine refresh must not start a cooldown, or a rotation
	// that happens straight after one goes unnoticed for a whole window.
	lastUnknownKid time.Time
}

func newKeySet(uri string, ttl time.Duration, client *http.Client) *keySet {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &keySet{uri: uri, ttl: ttl, client: client, now: time.Now}
}

// key returns the public key the token names, fetching or refreshing as
// needed.
//
// Every path that cannot produce a key returns an error. "Could not check" is
// not "fine" — the same rule the effect gate has followed since 001, arriving
// in a different package.
func (k *keySet) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	var justFetched bool
	if k.keys == nil || k.now().Sub(k.fetchedAt) >= k.ttl {
		err := k.refresh(ctx)
		switch {
		case err == nil:
			justFetched = true
		case k.keys == nil:
			// Nothing cached and nothing fetched: there is no key set in
			// force, so no token can be verified.
			return nil, err
		}
		// A failed refresh with a previous set in force keeps serving it. An
		// identity provider having an outage is not a reason to stop
		// accepting tokens it already signed.
	}

	if key, ok := k.keys[kid]; ok {
		return key, nil
	}

	// An unknown kid is either a rotation this set predates or a forgery. One
	// refetch tells them apart -- unless the set was fetched a moment ago as
	// part of this very call, in which case asking again could not tell us
	// anything new. The cooldown then stops the forgery case from setting the
	// pace of outbound requests.
	if !justFetched && k.now().Sub(k.lastUnknownKid) >= unknownKidCooldown {
		k.lastUnknownKid = k.now()
		if err := k.refresh(ctx); err != nil {
			return nil, err
		}
		if key, ok := k.keys[kid]; ok {
			return key, nil
		}
	}
	return nil, errs.Errorf(errs.ClassAuth, "inbound: no key %q in the issuer's key set", kid)
}

// refresh fetches and replaces the key set. The caller holds the mutex.
func (k *keySet) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.uri, http.NoBody)
	if err != nil {
		return errs.Errorf(errs.ClassAuth, "inbound: jwks: %w", err)
	}
	res, err := k.client.Do(req)
	if err != nil {
		return errs.Errorf(errs.ClassAuth, "inbound: jwks: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return errs.Errorf(errs.ClassAuth, "inbound: jwks: %s answered %d", k.uri, res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxJWKSBytes+1))
	if err != nil {
		return errs.Errorf(errs.ClassAuth, "inbound: jwks: read: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return errs.Errorf(errs.ClassAuth, "inbound: jwks: %s is larger than %d bytes", k.uri, maxJWKSBytes)
	}

	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}
	k.keys = keys
	k.fetchedAt = k.now()
	return nil
}

// jwk is the subset of a JSON Web Key this needs. Fields it does not
// understand are ignored rather than refused: a key set is allowed to carry
// keys for purposes that are none of lotsman's business.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS turns a key set document into verification keys.
func parseJWKS(body []byte) (map[string]crypto.PublicKey, error) {
	var document struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: %w", err)
	}
	if len(document.Keys) > maxJWKSKeys {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: more than %d keys", maxJWKSKeys)
	}

	keys := make(map[string]crypto.PublicKey, len(document.Keys))
	for i := range document.Keys {
		entry := &document.Keys[i]
		// `use: enc` is an encryption key. Verifying a signature with one
		// would be using a key for something its issuer said it is not for.
		if entry.Use == "enc" {
			continue
		}
		key, err := entry.publicKey()
		if err != nil {
			// One unusable key does not spoil the set: a provider may publish
			// a key type this build does not implement alongside ones it does.
			continue
		}
		keys[entry.Kid] = key
	}
	if len(keys) == 0 {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: no usable verification keys")
	}
	return keys, nil
}

// publicKey builds the verification key this JWK describes.
func (j *jwk) publicKey() (crypto.PublicKey, error) {
	switch j.Kty {
	case "RSA":
		n, err := decodeBigInt(j.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeBigInt(j.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() <= 0 {
			return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: unusable RSA exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: unsupported curve %q", j.Crv)
		}
		x, err := decodeCoordinate(j.X, curve)
		if err != nil {
			return nil, err
		}
		y, err := decodeCoordinate(j.Y, curve)
		if err != nil {
			return nil, err
		}
		// ParseUncompressedPublicKey does the on-curve check, which is the
		// reason to go through it rather than assembling the struct: a point
		// that is not on the curve is not a key, and finding that out later
		// is finding it out during a verification.
		key, err := ecdsa.ParseUncompressedPublicKey(curve, append([]byte{4}, append(x, y...)...))
		if err != nil {
			return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: %s point is not a key", j.Crv)
		}
		return key, nil
	default:
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: unsupported key type %q", j.Kty)
	}
}

// decodeCoordinate reads a base64url-encoded curve coordinate, left-padded to
// the curve's field size. JWK omits leading zeroes; the uncompressed point
// encoding requires them.
func decodeCoordinate(encoded string, curve elliptic.Curve) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: malformed key parameter")
	}
	size := (curve.Params().BitSize + 7) / 8
	if len(raw) > size {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: coordinate is too large for the curve")
	}
	padded := make([]byte, size)
	copy(padded[size-len(raw):], raw)
	return padded, nil
}

// decodeBigInt reads a base64url-encoded unsigned big-endian integer.
func decodeBigInt(encoded string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return nil, errs.Errorf(errs.ClassAuth, "inbound: jwks: malformed key parameter")
	}
	return new(big.Int).SetBytes(raw), nil
}
