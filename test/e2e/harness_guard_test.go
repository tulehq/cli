//go:build e2e

package e2e

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoHTTPTest fails if any Go file under test/e2e imports
// net/http/httptest. An httptest server stands in for a real one; the e2e
// tests call the real servers, started from the released images by tule up.
func TestNoHTTPTest(t *testing.T) {
	files := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		files++
		f, pErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if pErr != nil {
			return fmt.Errorf("parse imports: %w", pErr)
		}
		for _, imp := range f.Imports {
			p, uErr := strconv.Unquote(imp.Path.Value)
			if uErr != nil {
				return fmt.Errorf("%s: import %s: %w", path, imp.Path.Value, uErr)
			}
			if p == "net/http/httptest" {
				t.Errorf("%s imports net/http/httptest; run the real servers with tule up", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk test/e2e: %v", err)
	}
	if files == 0 {
		t.Fatal("found no Go files under test/e2e")
	}
}
