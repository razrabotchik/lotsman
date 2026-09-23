package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// catalogCacheTTL is how long a client may treat a tool list as fresh
// (FR-74).
//
// It is a hint and deliberately a short one. The catalog can be replaced
// under a running server (FR-72), so a generous TTL would be a promise this
// process is not in a position to make; a minute is long enough to spare a
// chatty client and short enough that a reload reaches it without anyone
// intervening.
const catalogCacheTTL = time.Minute

// digestMetaKey carries the catalog's identity in a tools/list result.
//
// The TTL says when to ask again; this says whether the answer changed. It is
// the honest half of the pair: a digest moves only when the published catalog
// does, which is exactly the promise reload keeps by refusing to republish an
// identical candidate.
const digestMetaKey = "lotsman/catalogDigest"

// methodListTools is the request whose result is worth caching. The SDK keeps
// its own constant unexported.
const methodListTools = "tools/list"

// addCacheHints attaches the hint and the digest to every tool listing.
//
// A middleware rather than a wrapper around the handler: the tool list is
// assembled by the SDK from what was registered, and there is no handler of
// ours to put this in.
func addCacheHints(srv *mcp.Server, digest string) {
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != methodListTools {
				return result, err
			}
			list, ok := result.(*mcp.ListToolsResult)
			if !ok {
				return result, err
			}
			list.TTLMs = int(catalogCacheTTL.Milliseconds())
			list.CacheScope = "public"
			if digest != "" {
				meta := list.GetMeta()
				if meta == nil {
					meta = map[string]any{}
				}
				meta[digestMetaKey] = digest
				list.SetMeta(meta)
			}
			return list, nil
		}
	})
}

// catalogDigest is the identity of what this server publishes, or empty when
// it publishes nothing but ping.
func (o *Options) catalogDigest() string {
	if o.Catalog == nil {
		return ""
	}
	return o.Catalog.Digest
}
