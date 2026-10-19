package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploadSpec is the reference operation of feature 009: one file part, one
// text part, and the types the document allows for the file.
const uploadSpec = `openapi: 3.1.0
info: { title: Catalog, version: "1.0" }
paths:
  /api/products/{id}/images:
    post:
      operationId: uploadProductImage
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              required: [file]
              properties:
                file: { type: string, format: binary }
                is_main: { type: boolean }
            encoding:
              file: { contentType: "image/jpeg, image/png" }
      responses:
        "201": { description: created }
`

func writeSpecFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileInputFixture writes a spec, a photo inside a root, and a configuration
// that allows reading it. extra is appended inside `execution.fileInputs`.
func fileInputFixture(t *testing.T, extra string) (spec, configPath, photo, root string) {
	t.Helper()
	spec = writeSpecFile(t, uploadSpec)
	root = t.TempDir()
	photo = filepath.Join(root, "front.png")
	if err := os.WriteFile(photo, png, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(t.TempDir(), "lotsman.yaml")
	config := fmt.Sprintf(`apiVersion: lotsman.dev/v1alpha1
execution:
  allowMutations: true
  baseURL: https://api.example.com
  interactiveApproval: never
  fileInputs:
    enabled: true
    roots: [%s]
    allowedContentTypes: [image/png]
%s`, root, extra)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return spec, configPath, photo, root
}

// explain-call is where an operator tests a path without touching the API: it
// resolves the files, shows the parts, and makes no request.
func TestExplainCallResolvesTheFilesWithoutSendingThem(t *testing.T) {
	t.Parallel()
	spec, configPath, photo, _ := fileInputFixture(t, "")
	args := writeArgs(t, fmt.Sprintf(`{"path":{"id":512},"body":{"file":{"path":%q},"is_main":true}}`, photo))

	stdout, stderr, code := runCLI(t, "explain-call", "upload_product_image",
		"--spec", spec, "--config", configPath, "--args", args)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	for _, want := range []string{
		"multipart/form-data",
		"file:   front.png",
		"image/png",
		"NO network request was made.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Each refusal class is answered at explain time, with its own code, and
// without a request.
func TestExplainCallNamesEveryFileRefusal(t *testing.T) {
	t.Parallel()
	spec, configPath, photo, root := fileInputFixture(t, "    maxFileBytes: 8\n    maxTotalBytes: 8\n")

	outside := filepath.Join(t.TempDir(), "id_rsa.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"outside the roots", fmt.Sprintf(`{"file":{"path":%q}}`, outside), "file_input_outside_roots"},
		{"over the limit", fmt.Sprintf(`{"file":{"path":%q}}`, photo), "file_input_too_large"},
		{"a relative path", `{"file":{"path":"front.png"}}`, "file_input_invalid_path"},
		{"a missing file", fmt.Sprintf(`{"file":{"path":%q}}`, filepath.Join(root, "nope.png")), "file_input_unreadable"},
		{"a filename that is a path", fmt.Sprintf(`{"file":{"path":%q,"filename":"a/b.png"}}`, photo), "file_input_invalid_filename"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := writeArgs(t, fmt.Sprintf(`{"path":{"id":512},"body":%s}`, tt.body))
			stdout, stderr, code := runCLI(t, "explain-call", "upload_product_image",
				"--spec", spec, "--config", configPath, "--args", args)
			if code != 0 {
				t.Fatalf("exit = %d: %s", code, stderr)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Errorf("output does not name %q:\n%s", tt.want, stdout)
			}
			if !strings.Contains(stdout, "NO network request was made.") {
				t.Errorf("explain-call did not say it sent nothing:\n%s", stdout)
			}
		})
	}
}

// Without the configuration the operation is published and refused, and the
// report says which line would change that.
func TestWithoutFileInputsTheUploadIsPublishedAndRefused(t *testing.T) {
	t.Parallel()
	spec := writeSpecFile(t, uploadSpec)

	stdout, stderr, code := runCLI(t, "inspect", spec, "--allow-mutations")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "file_inputs_disabled") {
		t.Errorf("inspect does not name the missing permission:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 supported") {
		t.Errorf("the operation was not translated:\n%s", stdout)
	}

	jsonOut, _, code := runCLI(t, "inspect", spec, "--allow-mutations", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"file_inputs_disabled", "execution.fileInputs"} {
		if !strings.Contains(jsonOut, want) {
			t.Errorf("the JSON report does not carry %q:\n%s", want, jsonOut)
		}
	}
}

// Over HTTP a remote client would be reading this machine's disk, so the
// permission has to be stated twice -- and `config check` says so out loud.
func TestTheHTTPTransportNeedsItsOwnPermission(t *testing.T) {
	t.Parallel()
	spec, configPath, _, _ := fileInputFixture(t, "")

	httpConfig := mustWrite(t, configPath, "server:\n  transport: http\n")
	stdout, _, code := runCLI(t, "inspect", spec, "--config", httpConfig)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "file_inputs_transport_not_allowed") {
		t.Errorf("the http transport published a file body without the opt-in:\n%s", stdout)
	}

	allowed, configStderr, code := runCLI(t, "config", "check", mustWrite(t, configPath, "    allowHTTPTransport: true\n"))
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, configStderr)
	}
	if !strings.Contains(allowed, "is usable") {
		t.Errorf("config check refused a usable configuration:\n%s", allowed)
	}
	if !strings.Contains(configStderr, "allowHTTPTransport") {
		t.Errorf("config check did not warn about the permission:\n%s", configStderr)
	}
}

// mustWrite appends a line inside the fileInputs block and returns a new path.
func mustWrite(t *testing.T, configPath, extra string) string {
	t.Helper()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(next, append(raw, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return next
}
