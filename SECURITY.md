# Security Policy

lotsman executes calls against third-party APIs on behalf of an LLM agent, so
security bugs here are the whole point of the project. Reports are welcome.

> **Status: pre-release (M0/M1).** Nothing is released yet; there is no supported
> version to patch. This file is a stub — it will be finalized before
> v0.1.0-alpha (T040).

## Supported versions

| Version | Supported |
|---------|-----------|
| `main`  | ✅ (pre-release, best effort) |

## Reporting a vulnerability

Please **do not** open a public issue for a suspected vulnerability.

1. Use GitHub private vulnerability reporting:
   <https://github.com/razrabotchik/lotsman/security/advisories/new>
2. Include: affected commit, an OpenAPI document or config that reproduces it
   (redact real credentials), the expected vs. actual behaviour, and impact.

Expect an acknowledgement within 7 days and a status update within 30 days while
the project is pre-release. Coordinated disclosure is the default: we agree on a
publication date once a fix is available.

## In scope

Anything that breaks a documented guarantee, in particular:

- an operation reaching the network although policy should have blocked it
  (read-only default, denied effect, rejected support verdict);
- a secret appearing in logs, errors, audit events, tool descriptions or tool
  results (`env:`/`file:` references are resolved only inside auth providers);
- argument values escaping their serialization slot: path traversal through a
  path parameter, CRLF header injection, query smuggling via unknown arguments;
- a malicious OpenAPI document causing resource exhaustion (alias bombs, ref
  cycles, unbounded responses) or reading files outside the spec root;
- SSRF: redirects, remote `$ref` or server variables reaching an origin the
  egress policy should have refused;
- prompt injection through unsanitized spec text that reaches the LLM context.

## Out of scope

- Anything that requires the operator to already be compromised (attacker-supplied
  config file, `execution.allowMutations` enabled on purpose, credentials handed
  to a hostile API).
- Vulnerabilities in an upstream API that lotsman merely calls.
- Findings against dependencies without a demonstrated impact on lotsman; report
  those upstream (we do run `govulncheck` in CI).
- MCP client behaviour we cannot enforce server-side: tool annotations and
  interactive approval are UX hints, never a security boundary (Constitution III).

## Hardening notes for operators

Defaults are fail-closed: read-only policy, redirects off, remote `$ref` off,
retries off. Keep them that way unless you have a reason, and prefer `env:`/
`file:` secret references — literal secrets in flags are rejected by design.
