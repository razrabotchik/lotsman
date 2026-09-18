package openapi

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// closure is the set of documents a spec pulls in through file references,
// after every one of them has been confined to the root directory.
type closure struct {
	// files are the confined documents, relative to the root, in a
	// deterministic order. They become the parser's allowlist: the rolodex is
	// told exactly which files it may read, instead of being pointed at a
	// directory and trusted to stay inside it.
	files []string
	// bytes is the total size of the closure, root document included.
	bytes int64
	// diagnostics report references that were refused. They are errors: a
	// document whose references cannot be followed is not a document lotsman
	// can serve, and `inspect` exists to say why.
	diagnostics []domain.Diagnostic
}

// resolveClosure walks the file references reachable from the root document.
//
// Every hop is confined to rootPath, resolved with symlinks expanded, because
// "./common.yaml" in an untrusted document is a request to read a file of the
// document's choosing. A reference that leaves the root is refused by name;
// so is one that points at something that is not a readable file.
//
// References resolve relative to the document that contains them, not to the
// root: a file in resources/account/ referring to ../shared.yaml means the
// sibling directory, and treating it as root-relative would silently read the
// wrong file.
func resolveClosure(rootBytes []byte, rootPath string, refs []refSite, limits Limits) (*closure, error) {
	result := &closure{bytes: int64(len(rootBytes))}
	if rootPath == "" {
		return result, nil // stdin: no directory to resolve anything against
	}

	// confine resolves symlinks on the candidate, so the root has to be
	// resolved too: a macOS temporary directory reaches /private/var through
	// /var, and a Windows one through an 8.3 short name. Comparing a resolved
	// path against an unresolved root refuses every legitimate $ref — safe,
	// and wrong. Callers going through specsource arrive resolved already;
	// this makes the comparison hold for the ones that do not.
	if resolved, err := filepath.EvalSymlinks(rootPath); err == nil {
		rootPath = resolved
	}

	type pending struct {
		abs  string
		refs []refSite
	}
	queue := []pending{{abs: filepath.Join(rootPath, "."), refs: refs}}
	seen := map[string]bool{}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		currentDir := current.abs
		if info, err := os.Stat(currentDir); err == nil && !info.IsDir() {
			currentDir = filepath.Dir(currentDir)
		}

		for _, ref := range current.refs {
			target := externalDocument(ref.value)
			if target == "" {
				continue // local reference
			}
			if isRemote(target) {
				result.diagnostics = append(result.diagnostics, diagnostic(domain.ReasonExternalRefUnsupported, ref,
					"remote references are not fetched; a document may not make lotsman issue a request of its choosing"))
				continue
			}

			abs, reason := confine(currentDir, rootPath, target)
			if reason != "" {
				result.diagnostics = append(result.diagnostics, diagnostic(reason, ref, confinementText(reason, rootPath, target)))
				continue
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true

			data, err := readConfined(abs)
			if err != nil {
				result.diagnostics = append(result.diagnostics, diagnostic(domain.ReasonRefUnresolvable, ref,
					fmt.Sprintf("reference %q: %v", target, err)))
				continue
			}

			result.bytes += int64(len(data))
			relative, relErr := filepath.Rel(rootPath, abs)
			if relErr != nil {
				result.diagnostics = append(result.diagnostics, diagnostic(domain.ReasonRefOutsideRoot, ref,
					confinementText(domain.ReasonRefOutsideRoot, rootPath, target)))
				continue
			}
			result.files = append(result.files, filepath.ToSlash(relative))

			if len(result.files)+1 > limits.maxRefDocuments() {
				return nil, errs.Errorf(errs.ClassUnsupported,
					"openapi: $ref closure spans more than %d documents", limits.maxRefDocuments())
			}
			if result.bytes > limits.maxRefBytes() {
				return nil, errs.Errorf(errs.ClassUnsupported,
					"openapi: $ref closure is larger than %d bytes", limits.maxRefBytes())
			}

			var node yaml.Node
			if err := yaml.Unmarshal(data, &node); err != nil {
				result.diagnostics = append(result.diagnostics, diagnostic(domain.ReasonRefUnresolvable, ref,
					fmt.Sprintf("%s is not valid YAML or JSON", relative)))
				continue
			}
			queue = append(queue, pending{abs: abs, refs: collectRefs(&node)})
		}
	}

	sort.Strings(result.files)
	return result, nil
}

// confine resolves target relative to fromDir and verifies that the result is
// inside root, with symlinks expanded: a symlink pointing out of the tree is
// the oldest way to escape a directory check.
func confine(fromDir, root, target string) (string, domain.ReasonCode) {
	if filepath.IsAbs(target) {
		// An absolute path ignores the root by construction.
		return "", domain.ReasonRefOutsideRoot
	}
	candidate := filepath.Clean(filepath.Join(fromDir, target))

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", domain.ReasonRefUnresolvable
		}
		return "", domain.ReasonRefUnresolvable
	}
	if !within(root, resolved) {
		return "", domain.ReasonRefOutsideRoot
	}
	return resolved, ""
}

// within reports whether path is root or sits inside it. The separator check
// is what stops "/srv/specs-evil" from passing as a child of "/srv/specs".
func within(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

func readConfined(abs string) ([]byte, error) {
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("referenced document cannot be read")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("referenced path is not a regular file")
	}
	// #nosec G304 -- abs was resolved and confined to the spec root above.
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("referenced document cannot be read")
	}
	return data, nil
}

func isRemote(target string) bool {
	return strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
}

func confinementText(reason domain.ReasonCode, root, target string) string {
	switch reason {
	case domain.ReasonRefOutsideRoot:
		return fmt.Sprintf("reference %q resolves outside the spec root %s; a document may not read files of its choosing", target, root)
	default:
		return fmt.Sprintf("reference %q is not a readable document inside the spec root %s", target, root)
	}
}

func diagnostic(code domain.ReasonCode, ref refSite, message string) domain.Diagnostic {
	return domain.Diagnostic{
		Severity: domain.SeverityError,
		Code:     code,
		Pointer:  ref.pointer,
		Line:     ref.node.Line,
		Col:      ref.node.Column,
		Message:  message,
	}
}
