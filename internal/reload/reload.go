package reload

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// Source builds a candidate catalog from whatever the operator pointed at.
//
// It is a function rather than a dependency so that this package knows
// nothing about OpenAPI: a reload must run the same pipeline startup ran, and
// the surest way to guarantee that is to be handed it.
type Source func(ctx context.Context) (*catalog.Catalog, error)

// Build turns a catalog into the server that publishes it.
type Build func(*catalog.Catalog) *mcp.Server

// Holder is the published catalog, and the only thing that ever changes it.
type Holder struct {
	source Source
	build  Build
	log    *slog.Logger

	// current is read on every request and written only by Reload.
	current atomic.Pointer[published]

	// reloading serializes candidates. Two reloads racing would both be
	// correct in isolation and could still publish the older one last.
	reloading sync.Mutex
}

// published is one catalog and the server built from it, kept together
// because a server that publishes a different catalog than the one reported
// is the disagreement this type exists to prevent.
type published struct {
	catalog *catalog.Catalog
	server  *mcp.Server
}

// New publishes an initial catalog. The catalog must already be built and
// valid: startup has its own error reporting, and a Holder that could be born
// empty would be a state every caller had to handle.
func New(initial *catalog.Catalog, source Source, build Build, log *slog.Logger) *Holder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &Holder{source: source, build: build, log: log}
	h.current.Store(&published{catalog: initial, server: build(initial)})
	return h
}

// Server returns the server as of now.
//
// A caller takes this once and holds the value for the whole of its request
// (§7.6 step 7): re-reading it mid-call could observe two catalogs in one
// request, which is the failure reload is supposed to prevent rather than
// introduce.
func (h *Holder) Server() *mcp.Server { return h.current.Load().server }

// Catalog returns the catalog as of now, on the same terms.
func (h *Holder) Catalog() *catalog.Catalog { return h.current.Load().catalog }

// Digest is the identity of what is published. It changes only when the
// published catalog does, which is what makes a cache hint honest (FR-74).
func (h *Holder) Digest() string { return h.current.Load().catalog.Digest }

// Reload builds a candidate and publishes it if it is both valid and
// different. It reports whether anything changed.
//
// Every failure mode leaves the working catalog serving and says what
// happened. That is the whole requirement: an operator who breaks a document
// should find out from the log, not from the clients.
func (h *Holder) Reload(ctx context.Context) (bool, error) {
	if h.source == nil {
		return false, errs.Errorf(errs.ClassInternal, "reload: no source to reload from")
	}
	h.reloading.Lock()
	defer h.reloading.Unlock()

	before := h.current.Load()
	candidate, err := h.source(ctx)
	if err != nil {
		h.log.Error("reload refused; the working catalog is unchanged",
			"class", string(errs.ClassOf(err)), "error", err, "digest", before.catalog.Digest)
		return false, err
	}
	if candidate.Digest == before.catalog.Digest {
		// Nothing substantive changed, so nothing is published. FR-74's cache
		// hints are a promise that a digest moves only when the catalog does,
		// and republishing an identical catalog would break it for no gain.
		h.log.Info("reload: catalog unchanged", "digest", candidate.Digest)
		return false, nil
	}

	h.current.Store(&published{catalog: candidate, server: h.build(candidate)})
	h.log.Info("catalog reloaded",
		"digest", candidate.Digest,
		"previous_digest", before.catalog.Digest,
		"tools", len(candidate.Tools),
		"previous_tools", len(before.catalog.Tools),
		"mode", string(candidate.Mode))
	return true, nil
}
