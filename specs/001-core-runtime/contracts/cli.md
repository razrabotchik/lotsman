# CLI Contract: Core Runtime

Stable surface for feature 001. Exit codes and `--json` schemas are contracts; breaking them
requires a version bump of the report schema.

## Commands

```text
lotsman serve SPEC [--config FILE] [--base-url URL] [--mode tools] [--lax]
                   [--read-only] [--log-level info] [--transport stdio]
lotsman inspect SPEC [--json] [--lax]
lotsman validate SPEC
lotsman operations SPEC [--supported|--rejected]
lotsman explain-call OPERATION_KEY --args FILE [--config FILE]
lotsman version [--json]
```

SPEC: file path or `-` (stdin). stdout of `serve` carries MCP protocol ONLY; humans read stderr.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime error (I/O, internal) |
| 2 | invalid usage / config error |
| 3 | spec invalid (parse/validation errors) |
| 4 | spec valid but zero supported operations (strict) |
| 5 | auth/secret resolution error |

`inspect` exits 0 even with rejected operations (report is the product); `--fail-on-rejected`
(CI helper) exits 4 when rejected > 0.

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
