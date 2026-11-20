package http_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
)

const adrReplayClasses = "../../../../docs/adr/0027-idempotency-replay-classes-a-retryable-refusal-is-not-stored.md"

func catalogueRows() []string {
	seen := map[string]bool{}
	var rows []string
	for _, e := range inboundhttp.ReplayCatalogue() {
		row := fmt.Sprintf("%s|%d|%s", e.Slug, e.Status, e.Class)
		if !seen[row] {
			seen[row] = true
			rows = append(rows, row)
		}
	}
	sort.Strings(rows)
	return rows
}

// TestReplayClassTableMatchesTheCatalogue holds the class table of ADR 0027
// equal to the catalogue: a problem type added, removed or reclassified in
// code without the ADR (or the other way round) is red.
func TestReplayClassTableMatchesTheCatalogue(t *testing.T) {
	raw, err := os.ReadFile(adrReplayClasses)
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\| (\\d{3}) \\| (FINAL|OUTCOME|RETRYABLE) \\|")
	seen := map[string]bool{}
	var adr []string
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		r := m[1] + "|" + m[2] + "|" + m[3]
		if seen[r] {
			t.Errorf("ADR 0027 lists %q twice", r)
		}
		seen[r] = true
		adr = append(adr, r)
	}
	sort.Strings(adr)
	if got, want := strings.Join(adr, "\n"), strings.Join(catalogueRows(), "\n"); got != want {
		t.Errorf("the ADR 0027 class table differs from the catalogue.\nADR:\n%s\n\ncatalogue:\n%s", got, want)
	}
}

// TestEveryProblemIsListedByTheCatalogueAccessor fails a problem declared
// anywhere in the package (a constructor call or a problem literal) whose slug
// the accessor does not list, so a problem type cannot dodge the class test by
// living outside the catalogue slice.
func TestEveryProblemIsListedByTheCatalogueAccessor(t *testing.T) {
	listed := map[string]bool{}
	for _, e := range inboundhttp.ReplayCatalogue() {
		listed[e.Slug] = true
	}
	declared := declaredProblemSlugs(t)
	if len(declared) < 60 {
		t.Fatalf("found only %d declared problem slugs: the scan is stale", len(declared))
	}
	for slug, where := range declared {
		if !listed[slug] {
			t.Errorf("problem %q is declared at %s but ReplayCatalogue does not list it: add it to the catalogue (or to the accessor's extras) with a replay class", slug, where)
		}
	}
	for slug := range listed {
		if _, ok := declared[slug]; !ok {
			t.Errorf("ReplayCatalogue lists %q but no declaration of it was found", slug)
		}
	}
}

// declaredProblemSlugs maps every slug declared through bad/notFound/conflict/
// unprocessable or a `problem{slug: ...}` literal in the package's production
// files to its position.
func declaredProblemSlugs(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			collectSlug(out, fset, n)
			return true
		})
	}
	return out
}

var problemConstructors = map[string]bool{"bad": true, "notFound": true, "conflict": true, "unprocessable": true}

// collectSlug records the slug n declares, if it is a constructor call or a
// problem literal.
func collectSlug(out map[string]string, fset *token.FileSet, n ast.Node) {
	switch x := n.(type) {
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && problemConstructors[id.Name] && len(x.Args) > 0 {
			record(out, fset, x.Args[0], x.Pos())
		}
	case *ast.CompositeLit:
		if id, ok := x.Type.(*ast.Ident); !ok || id.Name != "problem" {
			return
		}
		for _, el := range x.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "slug") {
				record(out, fset, kv.Value, x.Pos())
			}
		}
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func record(out map[string]string, fset *token.FileSet, e ast.Expr, pos token.Pos) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	slug, err := strconv.Unquote(lit.Value)
	if err != nil || slug == "" {
		return
	}
	out[slug] = fset.Position(pos).String()
}

// TestTheMiddlewareHoldsNoSlugList: the Idempotency-Key middleware and the
// class logic read the class from the catalogue entry and name no problem slug.
func TestTheMiddlewareHoldsNoSlugList(t *testing.T) {
	slugs := declaredProblemSlugs(t)
	for _, name := range []string{"idempotency.go", "replay_class.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					if _, isSlug := slugs[s]; isSlug {
						t.Errorf("%s names the problem slug %q: the middleware reads the class, never a slug", name, s)
					}
				}
			}
			return true
		})
	}
}

// TestOpenAPIListsTheRetryableRefusals holds the description of the
// Idempotency-Key parameter true: every RETRYABLE problem type below 500 is
// listed as "not stored", and no FINAL one is.
func TestOpenAPIListsTheRetryableRefusals(t *testing.T) {
	raw, err := os.ReadFile("../../../../apis/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "What is stored.")
	end := strings.Index(text[start+1:], "Rows are kept")
	if start < 0 || end < 0 {
		t.Fatal("the Idempotency-Key parameter description lost its \"What is stored.\" paragraph")
	}
	paragraph := text[start : start+1+end]
	cut := strings.Index(paragraph, "A refusal that depends on state that can change")
	if cut < 0 {
		t.Fatal("the paragraph no longer separates the stored refusals from the released ones")
	}
	stored, released := paragraph[:cut], paragraph[cut:]
	for _, e := range inboundhttp.ReplayCatalogue() {
		mention := "`" + e.Slug + "`"
		switch {
		case e.Class == "RETRYABLE" && e.Status < 500 && !strings.Contains(released, mention):
			t.Errorf("the OpenAPI description does not list RETRYABLE %s as not stored", e.Slug)
		case e.Class == "FINAL" && strings.Contains(released, mention):
			t.Errorf("the OpenAPI description lists FINAL %s as not stored", e.Slug)
		case e.Class == "RETRYABLE" && strings.Contains(stored, mention):
			t.Errorf("the OpenAPI description lists RETRYABLE %s as stored", e.Slug)
		}
	}
}
