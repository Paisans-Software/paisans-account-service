package pocketid

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The service is approved to make exactly two writes at the identity
// provider. This test fails on a third, on a write moved to another
// function, on any other way of spelling an HTTP write, on a changed path,
// and on any request body field beyond the approved ones. A failure is a
// question for the founder, not a test to fix.
func TestWriteSurface(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	writes := map[string]int{}
	var parsed []*ast.File
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
		parsed = append(parsed, file)
		for _, imp := range file.Imports {
			if imp.Path.Value == `"net/http"` && imp.Name != nil {
				t.Fatalf("%s imports net/http under an alias", f)
			}
		}
		for _, decl := range file.Decls {
			owner := "<package>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" {
						switch x.Sel.Name {
						case "MethodPost", "MethodPut", "MethodPatch", "MethodDelete", "MethodConnect", "MethodOptions", "MethodTrace":
							writes[owner+":"+x.Sel.Name]++
						}
					}
					switch x.Sel.Name {
					case "Post", "PostForm", "Head":
						t.Errorf("%s: %s is a request shortcut; the write surface goes through do", fset.Position(x.Pos()), x.Sel.Name)
					case "Method":
						// Reading a Method field is harmless; assignment is caught below.
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING {
						if s, err := strconv.Unquote(x.Value); err == nil {
							switch strings.ToUpper(s) {
							case "POST", "PUT", "PATCH", "DELETE":
								t.Errorf("%s: write method spelled as a string literal; use http.Method*", fset.Position(x.Pos()))
							}
						}
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok && id.Name == "Method" {
						t.Errorf("%s: a composite literal sets a Method field; requests are built in do", fset.Position(x.Pos()))
					}
				case *ast.AssignStmt:
					for _, l := range x.Lhs {
						if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Method" {
							t.Errorf("%s: assignment to a Method field", fset.Position(x.Pos()))
						}
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

	for _, file := range parsed {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			switch fn.Name.Name {
			case "CreateUser":
				call := onlyDoCall(t, fn)
				if got := stringLits(call.Args[2]); !reflect.DeepEqual(got, []string{"/api/users"}) {
					t.Fatalf("CreateUser path changed: %q", got)
				}
				if !bodyIsCreateUserRequest(call.Args[3], fn) {
					t.Fatal("CreateUser body must be a createUserRequest literal")
				}
			case "SendLoginLink":
				call := onlyDoCall(t, fn)
				got := stringLits(call.Args[2])
				if len(got) < 2 || got[0] != "/api/users/" || got[len(got)-1] != "/one-time-access-email" {
					t.Fatalf("SendLoginLink path changed: %q", got)
				}
				lit, ok := call.Args[3].(*ast.CompositeLit)
				if st, isStruct := func() (*ast.StructType, bool) {
					if !ok {
						return nil, false
					}
					s, k := lit.Type.(*ast.StructType)
					return s, k
				}(); !ok || !isStruct || len(st.Fields.List) != 0 || len(lit.Elts) != 0 {
					t.Fatal("SendLoginLink body must be an empty struct{}{}")
				}
			}
		}
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

// onlyDoCall returns the single c.do call in fn, which must have four arguments.
func onlyDoCall(t *testing.T, fn *ast.FuncDecl) *ast.CallExpr {
	t.Helper()
	var calls []*ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "do" {
				calls = append(calls, c)
			}
		}
		return true
	})
	if len(calls) != 1 || len(calls[0].Args) != 5 {
		t.Fatalf("%s must make exactly one c.do call with five arguments", fn.Name.Name)
	}
	return calls[0]
}

func stringLits(e ast.Expr) []string {
	var out []string
	ast.Inspect(e, func(n ast.Node) bool {
		if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
			s, _ := strconv.Unquote(l.Value)
			out = append(out, s)
		}
		return true
	})
	return out
}

func isCreateUserRequestLit(e ast.Expr) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	id, ok := lit.Type.(*ast.Ident)
	return ok && id.Name == "createUserRequest"
}

// bodyIsCreateUserRequest accepts a literal argument, or an identifier whose
// every assignment in fn is a createUserRequest literal.
func bodyIsCreateUserRequest(arg ast.Expr, fn *ast.FuncDecl) bool {
	if isCreateUserRequestLit(arg) {
		return true
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	found, clean := false, true
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			if lid, ok := l.(*ast.Ident); ok && lid.Name == id.Name {
				if len(as.Lhs) == len(as.Rhs) && isCreateUserRequestLit(as.Rhs[i]) {
					found = true
				} else {
					clean = false
				}
			}
		}
		return true
	})
	return found && clean
}
