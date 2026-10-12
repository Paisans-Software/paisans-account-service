package pocketid

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The service is approved to make exactly two writes at the identity
// provider. This test fails on a third, on a write moved to another
// function, and on any request body field beyond the approved ones. A
// failure is a question for the founder, not a test to fix.
func TestWriteSurface(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	writes := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if imp.Path.Value == `"net/http"` && imp.Name != nil {
				t.Fatalf("%s imports net/http under an alias", f)
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" {
						switch x.Sel.Name {
						case "MethodPost", "MethodPut", "MethodPatch", "MethodDelete":
							writes[fn.Name.Name+":"+x.Sel.Name]++
						}
					}
				case *ast.BasicLit:
					switch strings.Trim(x.Value, `"`) {
					case "POST", "PUT", "PATCH", "DELETE":
						t.Errorf("%s: write method spelled as a string literal; use http.Method*", fset.Position(x.Pos()))
					}
				}
				return true
			})
		}
	}
	want := map[string]int{"CreateUser:MethodPost": 1, "SendLoginLink:MethodPost": 1}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("write surface changed: got %v, want %v", writes, want)
	}
	var fields []string
	for i := 0; i < reflect.TypeOf(createUserRequest{}).NumField(); i++ {
		fields = append(fields, reflect.TypeOf(createUserRequest{}).Field(i).Tag.Get("json"))
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"displayName", "email", "emailVerified", "username"}) {
		t.Fatalf("createUserRequest fields changed: %v", fields)
	}
}
