# Quickstart: lotsman core (target UX for v0.1.0-alpha)

This is the 5-minute experience feature 001 must deliver (FR-76). It doubles as the e2e
scenario for T039 and the README skeleton for T036.

## 1. Inspect before you serve

```bash
lotsman inspect ./openapi.yaml
```

```text
OpenAPI 3.0.3 · digest sha256:9f2a…

Operations
──────────────────────────────
Found         84
Supported     71
Partial        4
Rejected       9

Rejected (top reasons)
──────────────────────────────
unsupported_media_type   5   (multipart/form-data)
unsupported_parameter_style 3 (deepObject)
ambiguous_security       1

Security
──────────────────────────────
remote refs     disabled
redirects       disabled
unknown mutations blocked (read-only default)
```

lotsman doesn't guess: the 9 rejected operations will not execute approximately — each has a
reason you can see with `lotsman operations ./openapi.yaml --rejected`.

## 2. Provide the API credential (reference, not literal)

```bash
export MYAPI_TOKEN="..."         # never passed as a flag
```

```yaml
# lotsman.yaml
authProfiles:
  default:
    scheme: bearer
    tokenRef: env:MYAPI_TOKEN
execution:
  defaultPolicy: read-only        # mutations require explicit opt-in
```

## 3. Register in Claude Desktop

`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "myapi": {
      "command": "lotsman",
      "args": ["serve", "/abs/path/openapi.yaml", "--config", "/abs/path/lotsman.yaml"]
    }
  }
}
```

Restart Claude Desktop → the API's read operations appear as tools.

## 4. Try it

Ask the agent: *"list the projects and show the two newest"* — it calls the GET tools.
Ask it to delete something — lotsman refuses before any network I/O:

```text
policy_denied: operation effect "destructive" blocked by read-only policy.
Enable with execution.allowMutations and an explicit allow rule.
```

## 5. Dry-run any call

```bash
lotsman explain-call "default:GET:/projects/{id}" --args '{"path":{"id":42}}'
```

Shows the exact request that would be sent (URL, encoded params, auth profile with redacted
secret) — without touching the network.
