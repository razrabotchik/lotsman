#!/usr/bin/env bash
#
# Can these tests fail?
#
# The suite says the security invariants hold. This asks the other question:
# would anyone notice if they stopped. Each mutation switches off one control
# in the source, runs the package that is supposed to guard it, and expects a
# test failure. A mutation nothing catches is a control nothing proves.
#
# It found one on the first run: a test that searched `json.Marshal` output
# for "<script>" and could never match, because encoding/json escapes "<".
#
# Two rules keep this honest:
#
#   - A pattern that no longer matches is a FAILURE, not a skip. The shape of
#     the code changes; a check that quietly stops checking is the thing this
#     script exists to find.
#   - A mutation that does not compile is a FAILURE too. "The compiler caught
#     it" proves nothing about the tests, and usually means the mutation was
#     written wrong.
#
# Usage: make mutate
set -uo pipefail
cd "$(dirname "$0")/.."

failures=0
backup=$(mktemp)
trap 'rm -f "$backup"' EXIT

# mutate NAME FILE FROM TO PACKAGE
mutate() {
	local name="$1" file="$2" from="$3" to="$4" pkg="$5"
	cp "$file" "$backup"

	if ! python3 - "$file" "$from" "$to" <<-'PY'
		import pathlib, sys
		path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
		p = pathlib.Path(path)
		s = p.read_text()
		if old not in s:
		    sys.exit(1)
		p.write_text(s.replace(old, new, 1))
	PY
	then
		printf '%-48s PATTERN GONE — update this mutation\n' "$name"
		failures=$((failures + 1))
		cp "$backup" "$file"
		return
	fi

	if ! go build ./... >/dev/null 2>&1; then
		printf '%-48s DOES NOT COMPILE — the mutation is wrong\n' "$name"
		failures=$((failures + 1))
	elif go test "$pkg" >/dev/null 2>&1; then
		printf '%-48s NOT CAUGHT\n' "$name"
		failures=$((failures + 1))
	else
		printf '%-48s caught\n' "$name"
	fi
	cp "$backup" "$file"
}

mutate "JWT: any algorithm the token names" \
	internal/inbound/oauth.go \
	"jwt.WithValidMethods(algorithms)," "jwt.WithValidMethods(nil)," \
	./internal/inbound/

mutate "JWT: no audience check" \
	internal/inbound/oauth.go \
	"jwt.WithAudience(audience)," "jwt.WithAudience(audience), jwt.WithoutClaimsValidation()," \
	./internal/inbound/

mutate "JWKS: no RSA size bounds" \
	internal/inbound/jwks.go \
	"if bits := n.BitLen(); bits < minRSABits || bits > maxRSABits {" \
	"if bits := n.BitLen(); bits < 0 {" \
	./internal/inbound/

mutate "bearer: any token is the configured one" \
	internal/inbound/inbound.go \
	"subtle.ConstantTimeCompare(got[:], want[:]) != 1" \
	"subtle.ConstantTimeCompare(got[:], want[:]) == -1" \
	./internal/inbound/

mutate "budget prune: walk property names again" \
	internal/mcpserver/search.go \
	"case domain.NamedSchemaKeywords[key]:" "case false:" \
	./internal/mcpserver/

mutate "schema: publish examples verbatim" \
	internal/catalog/inputschema.go \
	'case key == "examples":' "case false:" \
	./internal/catalog/

mutate "schema: publish descriptions verbatim" \
	internal/catalog/catalog.go \
	"return budgetBytes(sanitizeText(text), descriptionByteBudget)" "return text" \
	./internal/catalog/

mutate "display: show the raw path template" \
	internal/catalog/catalog.go \
	"return budgetBytes(sanitizeText(t.PathTemplate), maxPathDisplayBytes)" \
	"return t.PathTemplate" \
	./internal/catalog/

mutate "openapi: accept unrequestable paths" \
	internal/openapi/openapi.go \
	'if why := unrequestablePath(path); why != "" {' 'if why := ""; why != "" {' \
	./internal/openapi/

mutate "openapi: accept unpublishable parameter names" \
	internal/openapi/parameters.go \
	'if why := unpublishableName(key.name); why != "" {' 'if why := ""; why != "" {' \
	./internal/openapi/

mutate "runtime: skip the egress destination check" \
	internal/mcpserver/runner.go \
	"if denied := r.egress.CheckTarget(req.URL); denied != nil {" \
	"if denied := error(nil); denied != nil {" \
	./internal/mcpserver/

mutate "runtime: never ask for approval" \
	internal/mcpserver/approval.go \
	"return a.mode != config.ApprovalNever && !tool.Effect.IsRead()" "return false" \
	./internal/mcpserver/

echo
if [ "$failures" -ne 0 ]; then
	echo "$failures mutation(s) went unnoticed or could not be applied."
	exit 1
fi
echo "every mutation was caught."
