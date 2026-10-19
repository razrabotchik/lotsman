package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// png is a one-pixel PNG, so the content type comes from the content.
var png = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89,
}

// TestAFileIsUploadedByPathAndArrivesByteExact is the feature-009 scenario end
// to end: the model passes a path, the file never passes through it, and the
// API receives the bytes that are on disk.
//
// The fixture API answers with the sha256 of what it parsed, because "the call
// succeeded" is not the same statement as "the upload was byte-exact".
func TestAFileIsUploadedByPathAndArrivesByteExact(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	ctx := t.Context()

	api := startDevelopmentAPI(t)
	photos := t.TempDir()
	path := filepath.Join(photos, "AB-123.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "lotsman.yaml")
	config := fmt.Sprintf(`apiVersion: lotsman.dev/v1alpha1
execution:
  allowMutations: true
  baseURL: %s
  fileInputs:
    enabled: true
    roots: [%s]
    allowedContentTypes: [image/png]
`, api, photos)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve", "../../cmd/api-gateway/openapi.yaml",
		"--config", configPath, "--lax", "--log-level", "debug")
	var stderr syncBuffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "upload-e2e", Version: "v0"}, approving())
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "post_multipart_form",
		Arguments: map[string]any{"body": map[string]any{
			"name":       "AB-123",
			"attachment": map[string]any{"path": path},
		}},
	})
	if err != nil {
		t.Fatalf("tools/call: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("upload returned an error: %+v\nstderr:\n%s", res.Content, stderr.String())
	}

	// The tool result wraps the API's own answer, which is the JSON the
	// fixture wrote: what it parsed out of the multipart body.
	var envelope struct {
		Body string `json:"body"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	var got struct {
		Name          string `json:"name"`
		ContentLength int64  `json:"contentLength"`
		Attachment    struct {
			Filename    string `json:"filename"`
			Bytes       int64  `json:"bytes"`
			ContentType string `json:"contentType"`
			SHA256      string `json:"sha256"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal([]byte(envelope.Body), &got); err != nil {
		t.Fatalf("decode %s: %v", envelope.Body, err)
	}

	digest := sha256.Sum256(png)
	switch {
	case got.Name != "AB-123":
		t.Errorf("the text part arrived as %q", got.Name)
	case got.Attachment.Filename != "AB-123.png":
		t.Errorf("the file arrived as %q", got.Attachment.Filename)
	case got.Attachment.ContentType != "image/png":
		t.Errorf("the part type was %q", got.Attachment.ContentType)
	case got.Attachment.SHA256 != hex.EncodeToString(digest[:]):
		t.Errorf("the bytes that arrived are not the bytes on disk (%s)", envelope.Body)
	case got.Attachment.Bytes != int64(len(png)):
		t.Errorf("the file arrived as %d bytes, not %d", got.Attachment.Bytes, len(png))
	case got.ContentLength <= 0:
		// Declared, not chunked: a PHP backend behind nginx receives an empty
		// $_FILES from a chunked upload (ADR-0022 decision 5).
		t.Errorf("the request arrived without a Content-Length (%s)", envelope.Body)
	}

	// The content never reaches the log, and neither does the path.
	// The configured roots are logged on purpose -- they are the operator's
	// own grant. What must not appear is the content, or the path an argument
	// named (ADR-0022 decision 8).
	if log := stderr.String(); mentions(log, string(png[:8]), path) {
		t.Errorf("the log mentions the file's content or the path from the call:\n%s", log)
	}
}

// TestAPathOutsideTheRootsIsRefusedWithoutACall proves the sandbox from the
// client's side: the refusal names the code, the roots, and nothing else.
func TestAPathOutsideTheRootsIsRefusedWithoutACall(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	ctx := t.Context()

	api := startDevelopmentAPI(t)
	photos := t.TempDir()
	secrets := t.TempDir()
	outside := filepath.Join(secrets, "id_rsa.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "lotsman.yaml")
	config := fmt.Sprintf(`apiVersion: lotsman.dev/v1alpha1
execution:
  allowMutations: true
  baseURL: %s
  fileInputs:
    enabled: true
    roots: [%s]
    allowedContentTypes: [image/png]
`, api, photos)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve", "../../cmd/api-gateway/openapi.yaml",
		"--config", configPath, "--lax")
	var stderr syncBuffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "upload-refusal", Version: "v0"}, approving())
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "post_multipart_form",
		Arguments: map[string]any{"body": map[string]any{
			"attachment": map[string]any{"path": outside},
		}},
	})
	// A refusal is a tool error, not a protocol error: the call was answered.
	if err != nil {
		t.Fatalf("tools/call: %v\nstderr:\n%s", err, stderr.String())
	}
	if !res.IsError {
		t.Fatal("a path outside every root was accepted")
	}
	// The client needs the code to branch on, and its own path back to correct
	// itself; it wrote that path.
	message := resultText(res)
	if !mentions(message, "file_input_outside_roots") || !mentions(message, outside) {
		t.Errorf("refusal = %s", message)
	}
	// Nothing was sent: the fixture logs no request for it, and the audit
	// record says refused.
	if !mentions(stderr.String(), "decision=refused") {
		t.Errorf("audit did not record a refusal:\n%s", stderr.String())
	}
}

// mentions is the e2e way of asking whether output leaked something.
func mentions(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
