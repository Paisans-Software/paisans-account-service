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
					case "NewRequest", "NewRequestWithContext", "Do", "RoundTrip", "Get":
						if owner != "do" {
							t.Errorf("%s: %s outside do; only do may build or send a request", fset.Position(x.Pos()), x.Sel.Name)
						}
					case "Post", "PostForm", "Head":
						t.Errorf("%s: %s is a request shortcut; the write surface goes through do", fset.Position(x.Pos()), x.Sel.Name)
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
	structure(t, fset, parsed)
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
				checkCreateLiteral(t, fn)
				if !bodyIsCreateUserRequest(call.Args[3], fn) {
					t.Fatal("CreateUser body must be a createUserRequest literal")
				}
			case "SendLoginLink":
				call := onlyDoCall(t, fn)
				if !isLoginPath(call.Args[2], paramName(fn, 1)) {
					t.Fatal(`SendLoginLink path must be "/api/users/" + url.PathEscape(userID) + "/one-time-access-email"`)
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

func paramName(fn *ast.FuncDecl, i int) string {
	var names []string
	for _, f := range fn.Type.Params.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	if i < len(names) {
		return names[i]
	}
	return ""
}

func isStr(e ast.Expr, want string) bool {
	l, ok := e.(*ast.BasicLit)
	if !ok || l.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(l.Value)
	return err == nil && s == want
}

func isLoginPath(e ast.Expr, userID string) bool {
	outer, ok := e.(*ast.BinaryExpr)
	if !ok || outer.Op != token.ADD || !isStr(outer.Y, "/one-time-access-email") {
		return false
	}
	inner, ok := outer.X.(*ast.BinaryExpr)
	if !ok || inner.Op != token.ADD || !isStr(inner.X, "/api/users/") {
		return false
	}
	call, ok := inner.Y.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "PathEscape" {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "url" {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	return ok && userID != "" && arg.Name == userID
}

// fieldOf reports whether e is param.field, or strings.TrimSpace(param.field).
func fieldOf(e ast.Expr, param, field string, trim bool) bool {
	if c, ok := e.(*ast.CallExpr); ok && trim && len(c.Args) == 1 {
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "TrimSpace" {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "strings" {
				return fieldOf(c.Args[0], param, field, false)
			}
		}
		return false
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != field {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == param
}

// checkCreateLiteral requires every createUserRequest literal in CreateUser to
// be keyed, to set EmailVerified to the literal false (or omit it), and to take
// every other field only from the NewUser argument.
func checkCreateLiteral(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	param := paramName(fn, 1)
	n := 0
	ast.Inspect(fn, func(x ast.Node) bool {
		lit, ok := x.(*ast.CompositeLit)
		if !ok || !isCreateUserRequestLit(lit) {
			return true
		}
		n++
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				t.Fatal("createUserRequest literal must use keyed fields")
			}
			key := kv.Key.(*ast.Ident).Name
			good := false
			switch key {
			case "Username":
				good = fieldOf(kv.Value, param, "Username", true)
			case "Email":
				good = fieldOf(kv.Value, param, "Email", true)
			case "DisplayName":
				good = fieldOf(kv.Value, param, "DisplayName", true)
			case "EmailVerified":
				id, ok := kv.Value.(*ast.Ident)
				good = ok && id.Name == "false"
			}
			if !good {
				t.Fatalf("createUserRequest.%s is set from something other than the approved source", key)
			}
		}
		return true
	})
	if n != 1 {
		t.Fatalf("CreateUser must build exactly one createUserRequest, found %d", n)
	}
}

// structure is the allowlist half of the test: who may build and send
// requests, how do is called, and which methods the client exposes.
func structure(t *testing.T, fset *token.FileSet, files []*ast.File) {
	t.Helper()
	exported := map[string]bool{"UsernameTaken": true, "EmailTaken": true, "FindUser": true, "AdminEmails": true, "CreateUser": true, "SendLoginLink": true}
	posts := map[string]int{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.IsExported() && !(fn.Recv == nil && fn.Name.Name == "New") && !(fn.Recv != nil && exported[fn.Name.Name]) {
				t.Errorf("%s: exported %s is not part of the approved surface", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
		for _, decl := range file.Decls {
			owner := "<package>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			callFuns := map[ast.Expr]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callFuns[call.Fun] = true
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "do":
						m := ast.Expr(nil)
						if len(call.Args) > 1 {
							m = call.Args[1]
						}
						ms, ok := m.(*ast.SelectorExpr)
						id, isID := func() (*ast.Ident, bool) {
							if !ok {
								return nil, false
							}
							i, k := ms.X.(*ast.Ident)
							return i, k
						}()
						if !ok || !isID || id.Name != "http" || !strings.HasPrefix(ms.Sel.Name, "Method") {
							t.Errorf("%s: c.do must be given an http.Method* selector written in place", fset.Position(call.Pos()))
							return true
						}
						switch ms.Sel.Name {
						case "MethodGet":
						case "MethodPost":
							posts[owner]++
						default:
							t.Errorf("%s: c.do with %s", fset.Position(call.Pos()), ms.Sel.Name)
						}
					case "NewRequestWithContext":
						if owner == "do" {
							if len(call.Args) < 2 {
								t.Errorf("%s: NewRequestWithContext arguments", fset.Position(call.Pos()))
							} else if a, ok := call.Args[1].(*ast.Ident); !ok || a.Name != "method" {
								t.Errorf("%s: do must pass its method parameter to NewRequestWithContext", fset.Position(call.Pos()))
							}
						}
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "do" && !callFuns[sel] {
					t.Errorf("%s: do used other than by calling it", fset.Position(sel.Pos()))
				}
				return true
			})
		}
	}
	if !reflect.DeepEqual(posts, map[string]int{"CreateUser": 1, "SendLoginLink": 1}) {
		t.Errorf("c.do(http.MethodPost) calls: got %v", posts)
	}
}
