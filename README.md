# lotsman

Security-first OpenAPI → MCP runtime. One binary, no code generation. **Lotsman doesn't guess.**

Point it at an OpenAPI document and an MCP client — Claude Desktop, Claude Code, anything speaking
the protocol — and your agent can call that API. What it cannot translate exactly, it refuses, by
name, with a reason code you can act on.

> Status: **v0.1.0-alpha**. The local stdio runtime is real and tested against vendor
> specifications. HTTP transport, search mode and OAuth are not in this release — see
> [What is not here yet](#what-is-not-here-yet).

## Five minutes

```bash
# 1. Look before you serve. No network request is made.
lotsman inspect ./openapi.yaml

# 2. Serve it. Read-only by default; --base-url is the origin you authorize.
lotsman serve ./openapi.yaml --base-url https://api.example.com
```

Register it with Claude Code:

```bash
claude mcp add my-api -- lotsman serve /abs/path/openapi.yaml \
  --base-url https://api.example.com
claude mcp list          # expect: ✔ Connected
```

…or with Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "my-api": {
      "command": "/abs/path/to/lotsman",
      "args": ["serve", "/abs/path/openapi.yaml", "--base-url", "https://api.example.com"]
    }
  }
}
```

That is a read-only agent against one origin, with no credentials. Everything below is opt-in.

### Credentials

Secrets are **references**, never values — a token pasted into a flag is in your shell history and
your process table before it is anywhere useful.

```yaml
# lotsman.yaml
apiVersion: lotsman.dev/v1alpha1
authProfiles:
  bearer_auth:            # the name of the security scheme in your document
    scheme: bearer
    tokenRef: env:MY_API_TOKEN
```

```bash
export MY_API_TOKEN=…
lotsman serve ./openapi.yaml --config ./lotsman.yaml --base-url https://api.example.com
```

The value is read at the moment a request is made, registered for redaction, and applied by the
last code that touches the request before the wire. A canary-secret test suite checks that it
appears in no log, result, error or report.

### Large APIs: search mode

One tool per operation stops working above a certain catalog — not gradually, but completely: 65
Kubernetes operations serialize to **2.23 MB** of tool definitions, which is a model's whole context
spent on a menu. For those, lotsman publishes five tools instead of hundreds:

```bash
lotsman inspect ./openapi.yaml            # mode=search appears when the catalog does not fit
lotsman serve ./openapi.yaml --mode=search --base-url https://api.example.com
```

| | `tools/list` |
|---|---|
| Kubernetes `apps/v1`, 65 operations, tools mode | 2.23 MB |
| the same in search mode | **5.5 KB** |
| DigitalOcean, 631 operations, tools mode | 743 KB |
| the same in search mode | **5.5 KB** |

The five tools are `search_operations`, `describe_operation`, `list_tags`, `call_read_operation`
and `call_mutating_operation` (the last only when mutations are enabled). A search result is
discovery, never permission: every call re-checks identity, effect, arguments, auth and policy, and
`call_read_operation` refuses anything that is not a read whatever the search said.

`--mode` is `auto` by default: it follows the measurement (`catalog.maxSerializedBytes`, 120 KB).
Pin `--mode=tools` if you would rather have the full list and know it may not fit — lotsman will
serve it and say so.

### Letting the agent change things

```bash
lotsman serve ./openapi.yaml --config ./lotsman.yaml \
  --base-url https://api.example.com --allow-mutations
```

Without that flag, nothing whose effect is not a known `read` reaches the network — including
`POST`, `DELETE`, and a `GET` whose name contains a mutation-like verb (`GET /cache/rebuild` is
raised to `unknown` and refused).

Enabling mutations is not the same as enabling all of them, and it is not the same as running
them unattended. Each mutating call asks the user first, through the client:

```yaml
execution:
  allowMutations: true
  interactiveApproval: always     # always | client-capability | never
  denyRules:
    - { tag: billing }            # never, whatever else is enabled
  allowRules:
    - { effect: write }           # then only writes, and no destructive calls
```

`always` is the default. A client that cannot be asked — one that declared no elicitation
capability — gets a refusal before the network, not a call that quietly proceeds. That is a
safety prompt and **not** an authorization: it can stop a call the policy allowed, never permit
one the policy stopped. Use `--approval never` if your client cannot ask and you have decided
that is acceptable.

Ask before you enable any of it:

```bash
lotsman explain-call create_pet --spec ./openapi.yaml --args ./args.json \
  --config ./lotsman.yaml --base-url https://api.example.com
```

```text
operation   default:POST:/pets
effect      unknown (http_method, inferred)
policy      DENY (policy_unknown_effect_blocked: … enable execution.allowMutations …)
approval    required before the call; a client that cannot be asked is refused
request     POST https://api.example.com/pets
            egress: ALLOW
auth        profile "bearer_auth" → header Authorization: Bearer <env:MY_API_TOKEN>
media       application/json (required)
NO network request was made.
```

### Narrowing the catalog, and correcting it

An API with 631 operations rarely has 631 you need, and a heuristic that refuses to guess will
sometimes refuse something you know is safe. Both are configuration, and both are reported.

```yaml
catalog:
  includeTags: [droplets, images]   # publish these; everything else is excluded, and counted
operationOverrides:
  - match: { operationId: searchPets }
    effect: read                    # a reviewed statement: source=local_override, explicit
  - match: { method: GET, path: /legacy/rebuild-cache }
    enabled: false                  # not part of the surface at all
  - match: { operationId: createDraft }
    authProfile: personal           # which credential, when two could satisfy the operation
```

An override that matches no operation is a startup error, not a smaller catalog: a control you
believe is in force has to be in force. An override cannot publish something lotsman refused to
translate — it classifies, hides or binds, and support is not a matter of opinion.

### Serving it over HTTP

`serve --transport=http` publishes the stateless Streamable HTTP profile of MCP `2026-07-28`. No
protocol sessions, so no sticky routing: two replicas behind a round robin are indistinguishable
from one.

```yaml
server:
  transport: http
  listen: 0.0.0.0:8080            # a decision; the default is 127.0.0.1:8080
  allowedOrigins: [https://console.example.com]
  drainTimeout: 30s
  inboundAuth:
    mode: static-bearer           # none | static-bearer | oauth
    tokenRef: env:LOTSMAN_INBOUND_TOKEN
    trustedProxies: ["10.0.0.0/8"]
```

For production, put your own identity provider in front of it. lotsman is a resource server, not
an authorization server: it issues nothing and registers nobody.

```yaml
  inboundAuth:
    mode: oauth
    oauth:
      issuer: https://issuer.example.com
      resource: https://mcp.example.com/    # what a token's `aud` must carry
      jwksURI: https://issuer.example.com/jwks
      requiredScopes: [mcp:call]
      # …or, for opaque tokens, introspection instead of a key set — never both:
      # introspection:
      #   url: https://issuer.example.com/introspect
      #   clientID: lotsman
      #   clientSecretRef: env:LOTSMAN_INTROSPECTION_SECRET
```

Issuer, audience, expiry, scopes and signature are all checked, and the algorithm allowlist is
yours rather than the token's — a token does not get to nominate how it is verified. Every refusal
looks identical from outside: same status, same body, same challenge. The reason is in your log,
where you are.

`/.well-known/oauth-protected-resource` answers without a token, because a client reads it to find
out how to authenticate. It is the only route that does; the MCP endpoint and `/metrics` on the
same bind still refuse.

**A public bind with nothing authenticating it does not start.** Not a warning, not a 403 on the
first call — exit 2, before the socket opens. A deployment that comes up and then denies
everything looks like an outage; one that will not come up looks like what it is. If you mean it,
`--allow-unauthenticated-public-bind` says so out loud and warns on every start.

An inbound token is permission to talk to lotsman and nothing else. It is never forwarded upstream
and never widens what you configured.

The catalog can be replaced under a running server — `SIGHUP`, or `--watch` to poll the document.
A candidate that fails to parse leaves the working catalog serving and says why. `/metrics`
answers in the Prometheus text format, behind the same inbound authorization as everything else,
because the list of operations your agent calls is not public information.

In a container, remember the loopback default means nothing outside can connect: reaching the
endpoint is a deliberate `listen: 0.0.0.0:8080`, which then needs `inboundAuth`. The container
boundary is not a boundary lotsman can see.

```bash
make image && make image-check    # distroless, nonroot, read-only root filesystem
```

## What it supports today

Honest matrix. "Refused" means the operation is reported with a machine-readable reason and never
executed — not that it silently misbehaves.

| | Supported | Refused, with a reason |
|---|---|---|
| Document | OpenAPI 3.0, 3.1; YAML and JSON; file or stdin | Swagger 2.0 (`swagger: "2.0"` is named as out of scope) |
| References | local `$ref`, published as `$defs`; file `$ref` confined to the document's directory | remote `$ref` (never fetched); anything escaping the root |
| Parameters | path (`simple`), query (`form`, exploded or not), header (`simple`) — scalars and arrays of scalars | `deepObject`, `spaceDelimited`, `pipeDelimited`, `label`, `matrix`; objects and unions in a URL slot; cookie parameters (not serialized yet) |
| Bodies | `application/json` (and a sole `*/*`), objects, arrays, `allOf`/`oneOf`/`anyOf`, recursion | `application/x-www-form-urlencoded`, multipart, several competing media types, conditional subschemas |
| Auth | API key (header/query/cookie), HTTP basic, HTTP bearer — via `env:`/`file:` references | OAuth2, OpenID Connect, mutual TLS |
| Effects | `read` executes by default; `write`/`destructive`/`unknown` need `--allow-mutations` | — |

Measured against real documents ([docs/corpus.md](docs/corpus.md)): Kubernetes `apps/v1` 65 of 77
operations, DigitalOcean 631 of 659, Stripe 0 of 559 (form-urlencoded bodies and `deepObject`
parameters throughout — both named above).

## What it refuses on purpose

These are not gaps. They are the product.

- **A server URL from the document is not authorization.** You name the origin; the document does
  not get to.
- **Redirects are never followed.** Each hop would need the policy applied again, and a credential
  must not cross an origin.
- **A hostname that resolves into a private range is refused** unless you opt in — that is DNS
  rebinding, and `169.254.169.254` hands out cloud credentials to whoever asks. Naming
  `127.0.0.1` yourself is fine: that is intent.
- **An unknown argument is a validation error**, never something quietly dropped, and never
  something smuggled into a query string.
- **A truncated response is text**, marked `truncated` with `receivedBytes`. Half a JSON document
  handed over as structured content is how an agent ends up confidently wrong.
- **A `2 KB` YAML alias bomb is refused in milliseconds**, because expansion is measured rather
  than performed.

## Commands

```text
lotsman serve SPEC [--config FILE] [--base-url URL] [--lax] [--mode tools|search|auto]
                   [--read-only | --allow-mutations] [--allow-private-network]
                   [--approval always|client-capability|never]
                   [--transport stdio|http] [--listen HOST:PORT] [--watch]
                   [--drain-timeout D] [--allow-unauthenticated-public-bind]
lotsman inspect SPEC [--json] [--fail-on-rejected] [--config FILE] [--mode MODE]
lotsman validate SPEC [--quiet]
lotsman operations SPEC [--supported | --rejected] [--config FILE]
lotsman explain-call OPERATION --spec SPEC [--args FILE] [--config FILE]
lotsman version [--json]
```

Exit codes are a contract: `0` success, `1` runtime, `2` usage or configuration, `3` the document
is unusable, `4` valid but nothing is permitted or supported, `5` a credential could not be
resolved.

## What is not here yet

Upstream OAuth2 (M4), recipes, multi-API namespaces, cookie
parameters, form-urlencoded bodies, Swagger 2.0. No container image is published to a registry:
one is built and tested, but publishing needs a registry, a signing story and a retention policy.

`tools/list_changed` is not sent. Reload swaps the server a transport resolves per request, which
a stdio session never does — so the transport that reloads has nowhere to push a notification and
the one that could has nothing to announce ([ADR-0015](docs/adr/0015-catalog-reload.md)).
`tools/list` carries a TTL hint and the catalog digest instead.

Search mode ranks lexically (BM25 over names, paths, tags and summaries). Its recall is measured
rather than claimed: **Kubernetes Recall@5 1.00 / MRR 0.53, DigitalOcean 0.75 / 0.65**
([docs/corpus.md](docs/corpus.md)). Semantic retrieval is not in this release, and would need the
same benchmark to earn a claim.

## Building

```bash
make build          # bin/lotsman
make check          # lint + race tests
make corpus         # fetch the pinned vendor documents
make bench
```

Go ≥ 1.25. Four direct dependencies. Apache-2.0.

## Design notes

- [docs/spec.md](docs/spec.md) — the frozen product specification.
- [docs/pipeline.md](docs/pipeline.md) — how a document becomes tools, stage by stage.
- [docs/adr/](docs/adr/) — every decision that cost a debugging session, with the evidence.
- [docs/corpus.md](docs/corpus.md) — what lotsman makes of real vendor specifications.
