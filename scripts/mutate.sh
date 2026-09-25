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
# Each package runs under a timeout, because some of these mutations remove the
# very bound that stops a test from running for ever -- an unlimited read, a
# redirect loop. A timeout counts as caught: the test did notice. The bound is
# go test's own -timeout rather than a signal, so a hang reports itself instead
# of being killed and printing "Terminated" over the results.
#
# Usage: make mutate   (about two minutes: every mutation rebuilds a package
#                       and runs its tests)
set -uo pipefail
cd "$(dirname "$0")/.."

failures=0

# Every file a mutation touches is copied aside the first time it is touched,
# and everything copied is put back unconditionally on the way out -- normal
# exit, Ctrl-C, or a kill.
#
# Two lessons are built into that sentence, both learned the hard way here.
# The first version restored only the file it was working on, and only after
# the test it was waiting for returned, so an interrupted run walked away
# leaving a security control switched off in the working tree. The second
# version kept a hand-written list of the files to snapshot -- a second list
# that had to agree with the mutations, which it stopped doing the moment a
# mutation was added without its file. A file is therefore snapshotted by the
# code that mutates it, and by nothing else.
pristine=$(mktemp -d)

snapshot() {
	local file="$1"
	if [ ! -f "$pristine/$file" ]; then
		mkdir -p "$pristine/$(dirname "$file")"
		cp "$file" "$pristine/$file"
	fi
}

restore() {
	local changed=0 file
	while IFS= read -r -d '' file; do
		local original="${file#"$pristine/"}"
		if ! cmp -s "$file" "$original"; then
			cp "$file" "$original"
			changed=1
		fi
	done < <(find "$pristine" -type f -print0 2>/dev/null)
	[ "$changed" -eq 1 ] && echo "restored the working tree"
	rm -rf "$pristine"
	return 0
}
trap restore EXIT
trap 'restore; exit 130' INT TERM

# mutate NAME FILE FROM TO PACKAGE
mutate() {
	local name="$1" file="$2" from="$3" to="$4" pkg="$5"
	snapshot "$file"

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
		cp "$pristine/$file" "$file"
		return
	fi

	if ! go build "$pkg" >/dev/null 2>&1; then
		printf '%-48s DOES NOT COMPILE — the mutation is wrong\n' "$name"
		failures=$((failures + 1))
	elif timeout 180 go test -count=1 -timeout 100s "$pkg" >/dev/null 2>&1; then
		printf '%-48s NOT CAUGHT\n' "$name"
		failures=$((failures + 1))
	else
		printf '%-48s caught\n' "$name"
	fi
	cp "$pristine/$file" "$file"
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

mutate "egress: follow redirects" \
	internal/egress/policy.go \
	"return http.ErrUseLastResponse" "return nil" \
	./internal/egress/

mutate "egress: private ranges are ordinary addresses" \
	internal/egress/policy.go \
	"func isPrivate(ip net.IP) bool {" "func isPrivate(ip net.IP) bool { return false" \
	./internal/egress/

mutate "response: read a body of any size" \
	internal/response/response.go \
	"io.LimitReader(resp.Body, MaxBodyBytes+1)" "resp.Body" \
	./internal/response/

mutate "policy: a deny rule decides nothing" \
	internal/policy/gate.go \
	"	for _, rule := range c.Deny {" "	for _, rule := range []Rule(nil) {" \
	./internal/policy/

mutate "transport: a public bind needs nothing" \
	internal/httpserver/bind.go \
	"if loopback || authenticated || optIn {" "if loopback || true || authenticated || optIn {" \
	./internal/httpserver/

mutate "transport: any Host header is fine" \
	internal/httpserver/server.go \
	"if !permitted[strings.ToLower(r.Host)] {" "if false {" \
	./internal/httpserver/

mutate "transport: believe forwarding headers from anyone" \
	internal/inbound/forwarded.go \
	"if !fromTrustedProxy(r.RemoteAddr, trusted) {" \
	"if false && !fromTrustedProxy(r.RemoteAddr, trusted) {" \
	./internal/inbound/

mutate "reload: publish a candidate without comparing it" \
	internal/reload/reload.go \
	"if candidate.Digest == before.catalog.Digest {" "if false {" \
	./internal/reload/

mutate "headers: let a document parameterize Authorization" \
	internal/openapi/parameters.go \
	"if in == domain.LocationHeader && domain.IsProtectedHeader(key.name) {" "if false {" \
	./internal/openapi/

mutate "overrides: an override matching nothing is fine" \
	internal/catalog/overlay.go \
	"	claimed := make(map[domain.OperationKey]int, len(overrides))" \
	"	if true { return nil }; claimed := make(map[domain.OperationKey]int, len(overrides))" \
	./internal/catalog/

mutate "redaction: pass every string through unchanged" \
	internal/redact/redact.go \
	"func (r *Registry) String(s string) string {" \
	"func (r *Registry) String(s string) string { return s" \
	./internal/redact/

mutate "arguments: validate nothing" \
	internal/argvalidate/argvalidate.go \
	"func (v *Validator) Validate(args map[string]any) error {" \
	"func (v *Validator) Validate(args map[string]any) error { return nil" \
	./internal/argvalidate/

mutate "serialization: stop percent-encoding path values" \
	internal/requestbuild/serialize.go \
	"func percentEncode(s string, allowReserved bool) string {" \
	"func percentEncode(s string, allowReserved bool) string { return s" \
	./internal/requestbuild/

mutate "\$ref: a path outside the root is inside it" \
	internal/openapi/files.go \
	"func within(root, path string) bool {" \
	"func within(root, path string) bool { return true" \
	./internal/openapi/

mutate "search: a read tool may call a mutation" \
	internal/mcpserver/search.go \
	"		case kind == readOnlyCall && !isRead:" "		case false:" \
	./internal/mcpserver/

mutate "approval: proceed when the client cannot be asked" \
	internal/mcpserver/approval.go \
	"	if !clientCanBeAsked(req.Session) {" "	if false {" \
	./internal/mcpserver/

mutate "policy: an unknown effect is fine" \
	internal/policy/gate.go \
	'if effect.Effect == domain.EffectUnknown || effect.Effect == "" {' "if false {" \
	./internal/policy/

mutate "selection: an excluded tag filter selects everything" \
	internal/catalog/overlay.go \
	"	if len(tags) == 0 {" "	if true {" \
	./internal/catalog/

mutate "auth: pick the first of several satisfiable credentials" \
	internal/auth/select.go \
	"			Reason: domain.ReasonAmbiguousSecurity," \
	"			Reason: domain.ReasonCode(\"\")," \
	./internal/auth/

mutate "secrets: accept a literal instead of a reference" \
	internal/config/secret.go \
	"func ParseSecretRef(value string) (SecretRef, error) {" \
	"func ParseSecretRef(value string) (SecretRef, error) { return SecretRef(value), nil" \
	./internal/config/

echo
if [ "$failures" -ne 0 ]; then
	echo "$failures mutation(s) went unnoticed or could not be applied."
	exit 1
fi
echo "every mutation was caught."
