# CLI Contract: Core Runtime

Stable surface for feature 001. Exit codes and `--json` schemas are contracts; breaking them
requires a version bump of the report schema.

## Commands

```text
lotsman serve SPEC [--config FILE] [--base-url URL] [--mode tools] [--lax]
                   [--read-only | --allow-mutations] [--log-level info] [--transport stdio]
lotsman inspect SPEC [--json] [--allow-mutations] [--fail-on-rejected]
lotsman validate SPEC
lotsman operations SPEC [--supported|--rejected] [--allow-mutations]
lotsman explain-call OPERATION_KEY --args FILE [--config FILE]
lotsman version [--json]
```

SPEC: file path or `-` (stdin). stdout of `serve` carries MCP protocol ONLY; humans read stderr.

`serve` is read-only by default: only operations whose effect is `read` execute, and
`--allow-mutations` is required for anything else (FR-40/41). Every *supported* operation is
published whatever its effect -- publication is discovery, the gate decides execution -- with
annotations derived conservatively from the effect. Rejected operations are not published.

`serve` is strict by default: any rejected/partial operation prevents startup. `--lax` removes
those operations and serves the supported subset, but never clears execution/auth/policy blockers.
Document-level errors are fatal in both modes. During the M0 tracer bullet, real HTTP calls require
an explicit `--base-url`; this is replaced by the configured `allowedOrigins` policy in M1.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime error (I/O, internal) |
| 2 | invalid usage / config error |
| 3 | spec invalid (parse/validation errors) |
| 4 | spec valid but unsupported under the selected strictness policy |
| 5 | auth/secret resolution error |

`inspect` exits 0 even with rejected operations (report is the product); `--fail-on-rejected`
(CI helper) exits 4 when rejected > 0. It has no `--lax`: the report always accounts for every
operation, including the ones no mode would serve. Document-level errors are reported in
`documentIssues` rather than being fatal -- explaining a document `serve` refuses is what
`inspect` is for. `--allow-mutations` reports under that policy without enabling anything.

## `inspect --json` (schemaVersion "1")

```json
{
  "schemaVersion": "1",
  "lotsman": { "version": "0.1.0", "mcpProtocol": ["2026-07-28"] },
  "spec": { "source": "gitlab.yaml", "digest": "sha256:...", "openapi": "3.0.3" },
  "totals": { "found": 842, "supported": 731, "partial": 24, "rejected": 87 },
  "byReason": { "unsupported_media_type": 42, "unsupported_parameter_style": 19 },
  "catalog": { "mode": "tools", "toolCount": 731, "serializedBytesEstimate": 1843200, "digest": "sha256:..." },
  "security": { "remoteRefs": false, "redirects": false, "unknownMutationsBlocked": true },
  "operations": [
    {
      "key": "default:GET:/projects/{id}",
      "toolName": "get_project",
      "support": "supported",
      "published": true,
      "executable": true,
      "executionBlockers": [],
      "effect": { "value": "read", "source": "http_method", "confidence": "inferred", "warnings": [] },
      "reasons": []
    },
    {
      "key": "default:POST:/uploads",
      "support": "rejected",
      "reasons": [ { "code": "unsupported_media_type", "detail": "multipart/form-data", "pointer": "/paths/~1uploads/post" } ]
    }
  ]
}
```

Guarantees: `operations` sorted by key; unknown future fields must be ignored by consumers;
fields are only added, never repurposed, within schemaVersion 1.

## `explain-call` output (human, stderr-style)

```text
operation   default:GET:/projects/{id}/members
tool        list_project_members
effect      read (http_method, inferred)
policy      ALLOW (read-only mode: read permitted)
request     GET https://gitlab.example.com/api/v4/projects/42/members?page=2
            [query] page=2   [path] id=42 (encoded)
auth        profile "gitlab" → header PRIVATE-TOKEN: <redacted env:GITLAB_TOKEN>
media       -
NO network request was made.
```

## MCP surface (serve, mode=tools)

- One tool per supported operation; `inputSchema` = grouped object (path/query/headers/cookies/body),
  additionalProperties:false at every level.
- Annotations derived from effect conservatively (read → readOnlyHint; else potentially destructive).
- Tool result: `structuredContent` `{status, contentType, headers, body, truncated, receivedBytes}`;
  upstream 4xx/5xx → `isError: true` with bounded error body; policy/validation refusals are
  distinguishable from upstream errors by error code prefix (`policy_`, `validation_`, `upstream_`, `transport_`).
