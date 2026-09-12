package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
)

func TestParseWithTrailingSpecRejectsExtraPositionals(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseWithTrailingSpec(fs, []string{"one.yaml", "two.yaml"}); err == nil {
		t.Fatal("accepted an extra positional argument")
	}
}

func TestLoadCatalogFailsClosedOnDocumentErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.yaml")
	spec := []byte(`
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /widgets:
    get:
      parameters:
        - name: filter
          in: query
          schema: { $ref: '#/components/schemas/Missing' }
      responses:
        "200": { description: ok }
`)
	if err := os.WriteFile(path, spec, 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := loadCatalog(context.Background(), path, logger, true, catalog.Options{}); err == nil {
		t.Fatal("loadCatalog accepted a document with error diagnostics")
	}
}

func TestLoadCatalogStrictRejectsUnsupportedOperation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	spec := filepath.Join("..", "..", "testdata", "mini", "basic.yaml")
	if _, err := loadCatalog(context.Background(), spec, logger, false, catalog.Options{}); err == nil {
		t.Fatal("strict loadCatalog accepted a rejected operation")
	}
	if _, err := loadCatalog(context.Background(), spec, logger, true, catalog.Options{}); err != nil {
		t.Fatalf("lax loadCatalog rejected the supported subset: %v", err)
	}
}

func TestLoadCatalogRejectsZeroSupportedOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yaml")
	spec := []byte("openapi: 3.0.3\ninfo: { title: Empty, version: '1.0' }\npaths: {}\n")
	if err := os.WriteFile(path, spec, 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := loadCatalog(context.Background(), path, logger, true, catalog.Options{}); err == nil {
		t.Fatal("loadCatalog accepted a catalog with zero supported operations")
	}
}
