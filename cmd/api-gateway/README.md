# Lotsman development API

`api-gateway` is a deterministic local HTTP API for developing and testing
Lotsman. Despite its command name, it is not a reverse proxy or a production
gateway and should not be exposed to an untrusted network.

It belongs in the Lotsman repository because its behavior and OpenAPI fixture
must evolve together with the runtime features and integration tests.

## Run

From the repository root:

```bash
make run-api-gateway
```

The default address is `http://127.0.0.1:18080`. Use another address with:

```bash
go run ./cmd/api-gateway --addr 127.0.0.1:18081
```

The source OpenAPI document is
[`openapi.yaml`](./openapi.yaml). The running fixture also serves the same
document at `GET /openapi.yaml`, although Lotsman currently loads specs only
from a local file or stdin.

## Test the currently executable slice

Start the fixture in one terminal, then start Lotsman in another:

```bash
go run ./cmd/lotsman serve cmd/api-gateway/openapi.yaml \
  --base-url http://127.0.0.1:18080
```

`get_health` can execute now because it is a public, parameterless GET.
`get_redirect` is also published as executable, but Lotsman's egress client
must reject the redirect instead of following it.

Other endpoints deliberately cover work that remains on the roadmap:

| Endpoint | Capability exercised |
| --- | --- |
| `GET /health` | Parameterless public GET |
| `GET /v1/pets` | Query parameters and filtering |
| `GET /v1/pets/{petId}` | Path serialization and 404 responses |
| `POST /v1/pets` | JSON request body, mutation gate and API key |
| `DELETE /v1/pets/{petId}` | Destructive method and API key |
| `GET /v1/secure/profile` | Bearer authentication |
| `GET /v1/echo` | Arrays, query and header serialization |
| `GET /v1/status/{code}` | Caller-selected 4xx/5xx response handling |
| `GET /v1/redirect` | Redirect denial |
| `GET /v1/large` | Response-size limit and truncation metadata |
| `GET /v1/slow` | Request timeout and cancellation |

Demo credentials are intentionally public and valid only for this fixture:

- `X-API-Key: demo-secret`
- `Authorization: Bearer demo-token`

## Direct smoke checks

```bash
curl http://127.0.0.1:18080/health
curl 'http://127.0.0.1:18080/v1/pets?limit=1'
curl -H 'Authorization: Bearer demo-token' \
  http://127.0.0.1:18080/v1/secure/profile
curl -X POST -H 'X-API-Key: demo-secret' \
  -H 'Content-Type: application/json' \
  -d '{"name":"Rex","tags":["dog"]}' \
  http://127.0.0.1:18080/v1/pets
```
