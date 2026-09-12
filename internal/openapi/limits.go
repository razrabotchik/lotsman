package openapi

import "time"

// Limits bounds the work an untrusted document may cause in pipeline stages
// 1-3. Stage 0 (specsource) already bounded the bytes and the YAML node tree;
// these bound what parsing, $ref resolution and enumeration may expand that
// into. Zero values fall back to the defaults, so Limits{} is the safe
// configuration.
type Limits struct {
	// ParseTimeout bounds model construction end to end. libopenapi offers no
	// cancellation hook, so this is a deadline on waiting for it, not on the
	// work itself -- the byte and node budgets are what make the work finite.
	ParseTimeout time.Duration

	// MaxOperations caps paths x methods. The largest specs in the M0 corpus
	// (Kubernetes, GitLab) sit around a thousand operations; an order of
	// magnitude more is a generated document nobody meant to serve as tools.
	MaxOperations int

	// MaxRefDepth caps the length of a $ref chain (a -> b -> c). Real schemas
	// nest a handful deep; a long chain is either machine-generated noise or
	// an attempt to make resolution expensive.
	MaxRefDepth int

	// MaxRefDocuments and MaxRefBytes cap the ref closure: how many documents
	// may be pulled in and their total size. Today the closure is always the
	// single root document, because file and remote refs are refused
	// (ReasonExternalRefUnsupported); the accounting exists so T025 extends a
	// budget that is already enforced instead of adding one afterwards.
	MaxRefDocuments int
	MaxRefBytes     int64
}

// Limit defaults. See the field comments for the reasoning behind each.
const (
	DefaultParseTimeout    = 30 * time.Second
	DefaultMaxOperations   = 5_000
	DefaultMaxRefDepth     = 32
	DefaultMaxRefDocuments = 64
	DefaultMaxRefBytes     = 32 << 20 // 32 MiB across the whole ref closure
)

func (l Limits) parseTimeout() time.Duration {
	if l.ParseTimeout <= 0 {
		return DefaultParseTimeout
	}
	return l.ParseTimeout
}

func (l Limits) maxOperations() int {
	if l.MaxOperations <= 0 {
		return DefaultMaxOperations
	}
	return l.MaxOperations
}

func (l Limits) maxRefDepth() int {
	if l.MaxRefDepth <= 0 {
		return DefaultMaxRefDepth
	}
	return l.MaxRefDepth
}

func (l Limits) maxRefDocuments() int {
	if l.MaxRefDocuments <= 0 {
		return DefaultMaxRefDocuments
	}
	return l.MaxRefDocuments
}

func (l Limits) maxRefBytes() int64 {
	if l.MaxRefBytes <= 0 {
		return DefaultMaxRefBytes
	}
	return l.MaxRefBytes
}
