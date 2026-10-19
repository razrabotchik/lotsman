package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/fileinput"
)

// A file is the one argument a person cannot check by its name: the bytes come
// off this machine. The prompt therefore says which file, how big and what it
// turned out to be -- and not the full path, which describes the host, and not
// a hash, which would have to come from a second read of a file that may have
// changed since (ADR-0022 decision 7).
func TestTheApprovalPromptDescribesTheFilesItWouldSend(t *testing.T) {
	const content = "\x89PNG\r\n\x1a\n" + "CANARY-file-content-DO-NOT-LEAK"

	root := t.TempDir()
	path := filepath.Join(root, "front.png")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               []string{root},
		AllowedContentTypes: []string{"image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := resolver.Session()
	defer func() { _ = session.Close() }()
	file, err := session.Open(fileinput.Request{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	tool := &catalog.Tool{
		Name:         "upload_product_image",
		OperationKey: "default:POST:/api/products/{id}/images",
		Method:       "POST",
		PathTemplate: "/api/products/{id}/images",
		Effect:       domain.EffectDecision{Effect: domain.EffectWrite},
	}
	message := approvalMessage(tool, "https://api.test", map[string]any{
		domain.GroupPath: map[string]any{"id": 512},
		domain.GroupBody: map[string]any{"file": map[string]any{"path": path}, "is_main": true},
	}, []*fileinput.File{file})

	for _, want := range []string{"front.png", "image/png", "upload_product_image"} {
		if !strings.Contains(message, want) {
			t.Errorf("the prompt does not say %q:\n%s", want, message)
		}
	}
	if !strings.Contains(message, humanBytes(file.Size())) {
		t.Errorf("the prompt does not say how big the file is:\n%s", message)
	}
	if strings.Contains(message, root) {
		t.Errorf("the prompt carries the full path:\n%s", message)
	}
	if strings.Contains(message, "CANARY-file-content") {
		t.Errorf("the prompt carries the file's content:\n%s", message)
	}
	if strings.Contains(message, file.SHA256()) && file.SHA256() != "" {
		t.Errorf("the prompt carries a hash of something that has not been sent:\n%s", message)
	}
}

func TestSizesAreReadableAtAGlance(t *testing.T) {
	for _, tt := range []struct {
		bytes int64
		want  string
	}{
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{20 << 20, "20.0 MiB"},
	} {
		if got := humanBytes(tt.bytes); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
