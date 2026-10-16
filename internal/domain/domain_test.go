package domain

import "testing"

func TestNewOperationKey(t *testing.T) {
	tests := []struct {
		name         string
		namespace    string
		method       string
		pathTemplate string
		want         OperationKey
	}{
		{"empty namespace defaults", "", "get", "/pets", "default:GET:/pets"},
		{"namespace and lowercase method", "petstore", "post", "/pets/{petId}", "petstore:POST:/pets/{petId}"},
		{"method already uppercase", "ns", "DELETE", "/pets/{petId}", "ns:DELETE:/pets/{petId}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewOperationKey(tt.namespace, tt.method, tt.pathTemplate)
			if got != tt.want {
				t.Errorf("NewOperationKey(%q, %q, %q) = %q, want %q",
					tt.namespace, tt.method, tt.pathTemplate, got, tt.want)
			}
		})
	}
}

// An overlay changes what was parsed, so it is part of the manifest: with one
// the digest differs, in another order it differs again, and with none it is
// what it always was.
func TestManifestDigestFoldsInOverlays(t *testing.T) {
	refs := []RefDocument{{Path: "pets.yaml", Digest: "sha256:aa"}}
	a := RefDocument{Path: "fixes/a.yaml", Digest: "sha256:01"}
	b := RefDocument{Path: "../shared/a.yaml", Digest: "sha256:01"}
	plain := ManifestDigest("sha256:root", refs, nil)
	if plain != ManifestDigest("sha256:root", refs, []RefDocument{}) {
		t.Fatal("an empty overlay list changed the digest")
	}
	withA := ManifestDigest("sha256:root", refs, []RefDocument{a})
	if withA == plain {
		t.Fatal("an overlay left the manifest digest unchanged")
	}
	if ManifestDigest("sha256:root", refs, []RefDocument{b}) == withA {
		t.Fatal("two overlays with the same bytes in different places gave one digest")
	}
	if ManifestDigest("sha256:root", refs, []RefDocument{a, b}) == ManifestDigest("sha256:root", refs, []RefDocument{b, a}) {
		t.Fatal("overlay order is not part of the digest")
	}
	// An overlay is not a referenced document of the same name.
	if ManifestDigest("sha256:root", []RefDocument{a}, nil) == withA {
		t.Fatal("an overlay and a reference with the same path and bytes gave one digest")
	}
}
