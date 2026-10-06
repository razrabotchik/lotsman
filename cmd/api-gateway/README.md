# Lotsman development API

`api-gateway` is a deterministic local HTTP API for developing and testing
Lotsman. Despite its command name, it is not a reverse proxy or a production
gateway and should not be exposed to an untrusted network.

It belongs in the Lotsman repository because its behavior and OpenAPI fixture
must evolve together with the runtime features and integration tests. Nothing
here imports `internal/`: the fixture must be able to disagree with the
runtime, which is the only way it can test it.

## Run

From the repository root:

```bash
make run-api-gateway
```

Two listeners come up:

| | address | why both |
| --- | --- | --- |
| HTTP | `http://127.0.0.1:18080` | the API itself |
| HTTPS | `https://127.0.0.1:18443` | Lotsman refuses a plaintext token endpoint — `authProfiles[].tokenURL` must be https, loopback included |

The TLS listener uses a self-signed certificate written to the directory given
by `--cert-dir` (the system temporary directory by default) and **reused across
restarts**, so a client that was told to trust it keeps working when the fixture
is restarted. The private key is written beside it at `0600`; that is a
development key for a loopback listener, and it is the price of restarts not
breaking trust. Point a client at the certificate:

```bash
SSL_CERT_FILE=/tmp/api-gateway-ca.pem lotsman serve ...
curl --cacert /tmp/api-gateway-ca.pem https://127.0.0.1:18443/health
```

Other addresses:

```bash
go run ./cmd/api-gateway --addr 127.0.0.1:18081 --tls-addr '' --cert-dir /tmp
```

The source OpenAPI document is [`openapi.yaml`](./openapi.yaml), also served at
`GET /openapi.yaml`, although Lotsman loads specs only from a local file or
stdin.

Demo credentials are intentionally public and valid only for this fixture:

- `X-API-Key: demo-secret`
- `Authorization: Bearer demo-token`
- client credentials `demo-client` / `demo-client-secret`

## What each endpoint is for

The document declares 38 operations. Lotsman supports 33 of them, refuses 5,
and blocks more by policy — so `lotsman inspect cmd/api-gateway/openapi.yaml`
prints a support matrix against a real API rather than against prose.

### The basics

| Endpoint | What it exercises |
| --- | --- |
| `GET /health` | Parameterless public GET |
| `GET /v1/pets` | Query parameters and filtering |
| `GET /v1/pets/{petId}` | Path serialization and 404 responses |
| `POST /v1/pets` | JSON body, the mutation gate, an API key |
| `PUT /v1/pets/{petId}` | A write effect read from the method |
| `PATCH /v1/pets/{petId}` | One media type, and not `application/json` |
| `DELETE /v1/pets/{petId}` | A destructive method |
| `POST /v1/pets/{petId}/actions/rename` | A POST whose effect the method cannot reveal: unknown, and refused until the configuration says otherwise |
| `GET /v1/secure/profile` | Bearer authentication from an `env:` reference |
| `GET /v1/echo` | Arrays, query and header serialization |
| `GET /v1/status/{code}` | Caller-selected 4xx/5xx handling |
| `GET /v1/slow` | Request timeout and cancellation |
| `GET /v1/large` | The response byte cap and its truncation metadata |

### The mirror — what was actually sent

`GET|POST /v1/mirror/{segment}` reflects the request: method, decoded and raw
path, path segments, raw query, repeated query keys, headers, cookies and body.
It answers the questions a report cannot:

```bash
# Did the path value stay inside one segment, or become a new one?
lotsman explain-call mirror_request --spec cmd/api-gateway/openapi.yaml ...
# segments: ["v1","mirror","a%2Fb"] -- encoded, one segment. Three entries, not four.
```

Credentials are reported as present and never as themselves: a fixture that
printed one into a terminal would teach the wrong habit.

### Response shapes

| Endpoint | The decision it forces |
| --- | --- |
| `GET /v1/shapes/no-content` | 204 with no body: "no content" must not read as "no answer" |
| `GET /v1/shapes/text` | A body that is not JSON and does not claim to be |
| `GET /v1/shapes/binary` | Bytes that are not text, including a NUL and invalid UTF-8 |
| `GET /v1/shapes/cookie` | `Set-Cookie` and `Authorization` in a *response*: neither may reach a model |
| `GET /v1/shapes/rate-limited` | 429 with `RateLimit-*`: on the allowlist, because a model can act on them |
| `GET /v1/shapes/problem` | `application/problem+json`, an error body worth showing verbatim |
| `GET /v1/shapes/gzip` | A compressed body: the byte cap must measure the body, not the wire |
| `GET /v1/shapes/drip` | A body sent chunk by chunk: the idle phase of a time budget |
| `GET /v1/shapes/headers` | Many response headers and one long one |
| `GET /v1/redirect` | Redirect denial |
| `GET /v1/redirect/loop` | A redirect to itself: never followed, so never a loop |
| `GET /v1/redirect/chain` | A counted chain, so a hop limit is observable |
| `GET /v1/redirect/external` | A redirect to another origin: the reason the guard exists |

### A credential Lotsman obtains rather than reads

The client-credentials flow, end to end, which is otherwise the hardest part of
the runtime to try by hand.

| Endpoint | What it shows |
| --- | --- |
| `POST /oauth/token` | The mint itself: grant type, client authentication (basic or body), scopes |
| `GET /oauth/stats` | How many tokens were minted, and what the last mint asked for |
| `GET /v1/minted/profile` | A call that presents a minted token |
| `GET /v1/minted/stale-once` | Refuses the first call ever: the re-mint and retry **succeed** |
| `GET /v1/minted/always-stale` | Refuses every new token once: the retry happens **exactly once** and the second 401 belongs to the caller |

The counters are what make it provable. With one profile and two calls:

```text
minted 1, two 200s          -- the token was cached, not re-minted per call
stale-once: minted 2, 200   -- one 401, one re-mint, one retry, and it worked
always-stale: minted 3, 401 -- one extra mint per call. Never a loop.
```

### Declared on purpose, refused on purpose

These exist so a refusal can be read in context. The endpoints work: when one
of these becomes supported, the fixture does not have to be written — the
operations move from rejected to executable.

| Endpoint | Reason code |
| --- | --- |
| `POST /v1/forms/urlencoded` | `unsupported_media_type` — the Stripe shape |
| `POST /v1/forms/multipart` | `unsupported_media_type` |
| `PATCH /v1/pets/{petId}/competing` | `unsupported_media_type` — four patch types at once, the Kubernetes shape |
| `GET /v1/filters` | `unsupported_parameter_style` ×3 — `deepObject`, `spaceDelimited`, `pipeDelimited` |
| `POST /v1/conditional` | `unsupported_body_schema` — `if`/`then`/`else`, what 21 DigitalOcean operations are refused for |
| `GET /v1/session` | `parameters_not_implemented` — a cookie parameter: published, never executable, which is a different answer from rejected |
| `POST /v1/variants` | none: `oneOf` **is** supported, and it is here so the difference from the line above is visible |

## A stand in two terminals

```bash
# 1. the fixture
make run-api-gateway

# 2. lotsman over it
cat > /tmp/stand.yaml <<'EOF'
apiVersion: lotsman.dev/v1alpha1
spec: {source: cmd/api-gateway/openapi.yaml, strict: false}
execution:
  allowedOrigins: ["http://127.0.0.1:18080", "https://127.0.0.1:18443"]
  allowMutations: true
  interactiveApproval: never
  maxResponseBytes: 4096
authProfiles:
  apiKey: {scheme: apikey, in: header, name: X-API-Key, tokenRef: env:STAND_API_KEY}
  bearer: {scheme: bearer, tokenRef: env:STAND_BEARER}
  serviceAuth:
    scheme: oauth2-client-credentials
    tokenURL: https://127.0.0.1:18443/oauth/token
    clientID: demo-client
    clientSecretRef: env:STAND_CLIENT_SECRET
    scopes: [pets:read]
operationOverrides:
  - match: {operationId: renamePet}
    effect: write
server: {transport: http, listen: "127.0.0.1:8080", logLevel: debug}
EOF

SSL_CERT_FILE=/tmp/api-gateway-ca.pem \
STAND_API_KEY=demo-secret STAND_BEARER=demo-token \
STAND_CLIENT_SECRET=demo-client-secret \
  go run ./cmd/lotsman serve --config /tmp/stand.yaml
```

The server's own view of the document:

```bash
go run ./cmd/lotsman inspect cmd/api-gateway/openapi.yaml --json | \
  python3 -c 'import json,sys; r=json.load(sys.stdin); print(r["totals"], r["byReason"])'
```

## Direct smoke checks

```bash
curl http://127.0.0.1:18080/health
curl 'http://127.0.0.1:18080/v1/pets?limit=1'
curl -H 'Authorization: Bearer demo-token' http://127.0.0.1:18080/v1/secure/profile
curl -X POST -H 'X-API-Key: demo-secret' -H 'Content-Type: application/json' \
  -d '{"name":"Rex","tags":["dog"]}' http://127.0.0.1:18080/v1/pets
curl 'http://127.0.0.1:18080/v1/mirror/a%2Fb?tag=x&tag=y'
curl --cacert /tmp/api-gateway-ca.pem -X POST https://127.0.0.1:18443/oauth/token \
  -d 'grant_type=client_credentials&client_id=demo-client&client_secret=demo-client-secret'
```
