package pocketid

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var allowedImports = map[string]bool{
	"bytes": true, "context": true, "encoding/json": true, "errors": true, "fmt": true,
	"io": true, "log/slog": true, "net/http": true, "net/url": true, "strings": true,
	"github.com/paisans-software/paisans-invite/internal/idp": true,
}

var httpWriteConsts = map[string]bool{
	"MethodPost": true, "MethodPut": true, "MethodPatch": true, "MethodDelete": true,
	"MethodConnect": true, "MethodOptions": true, "MethodTrace": true,
}

func isNetHTTP(o types.Object) bool {
	return o != nil && o.Pkg() != nil && o.Pkg().Path() == "net/http"
}

// recvName is the bare type name of a method's receiver, or "" for a function.
func recvName(f *types.Func) string {
	r := f.Type().(*types.Signature).Recv()
	if r == nil {
		return ""
	}
	t := r.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return "?"
}

// The service is approved to make exactly two writes at the identity
// provider. This test is type-checked, so a shadowed name or a look-alike
// function cannot stand in for net/http. It fails on a third write, on a write
// moved to another function, on any other way of building or sending a request,
// on an import that could open a connection by another route, on a changed
// path, and on any request body field beyond the approved ones. A failure is a
// question for the founder, not a test to fix.
func TestWriteSurface(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
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
			if p, _ := strconv.Unquote(imp.Path.Value); !allowedImports[p] {
				t.Errorf("%s: import %s is not on the allowlist", fset.Position(imp.Pos()), p)
			}
		}
	}
	info := &types.Info{
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("github.com/paisans-software/paisans-invite/internal/idp/pocketid", fset, parsed, info)
	if err != nil {
		t.Fatalf("type-checking the package: %v", err)
	}

	isClientDo := func(fn *ast.FuncDecl) bool {
		if fn.Recv == nil || fn.Name.Name != "do" {
			return false
		}
		f, ok := info.Defs[fn.Name].(*types.Func)
		return ok && types.TypeString(f.Type().(*types.Signature).Recv().Type(), nil) == "*"+pkg.Path()+".Client"
	}

	writes := map[string]int{}
	posts := map[string]int{}
	exported := map[string]bool{"UsernameTaken": true, "EmailTaken": true, "FindUser": true, "AdminGroupEmails": true, "CreateUser": true, "SendLoginLink": true}
	for _, file := range parsed {
		for _, decl := range file.Decls {
			owner, inDo := "<package>", false
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner, inDo = fn.Name.Name, isClientDo(fn)
				if fn.Name.IsExported() && !(fn.Recv == nil && fn.Name.Name == "New") && !(fn.Recv != nil && exported[fn.Name.Name]) {
					t.Errorf("%s: exported %s is not part of the approved surface", fset.Position(fn.Pos()), fn.Name.Name)
				}
				if inDo {
					checkDoBody(t, fset, info, fn)
				}
			}
			callFuns := map[ast.Node]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					checkHTTPUse(t, fset, info, x, owner, inDo, writes)
				case *ast.BasicLit:
					if x.Kind == token.STRING {
						if s, err := strconv.Unquote(x.Value); err == nil {
							switch strings.ToUpper(s) {
							case "POST", "PUT", "PATCH", "DELETE":
								t.Errorf("%s: write method spelled as a string literal; use http.Method*", fset.Position(x.Pos()))
							}
						}
					}
				case *ast.CallExpr:
					callFuns[x.Fun] = true
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						callFuns[sel.Sel] = true
					}
					if isPkgDo(info, x.Fun, pkg) {
						checkDoCall(t, fset, info, x, owner, posts)
					}
				case *ast.AssignStmt:
					for _, l := range x.Lhs {
						checkEmailVerifiedAssign(t, fset, info, l)
					}
				case *ast.IncDecStmt:
					checkEmailVerifiedAssign(t, fset, info, x.X)
				case *ast.UnaryExpr:
					if x.Op == token.AND {
						checkEmailVerifiedAssign(t, fset, info, x.X)
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && isPkgDoObj(info.Uses[id], pkg) && !callFuns[id] {
					t.Errorf("%s: do used other than by calling it", fset.Position(id.Pos()))
				}
				return true
			})
		}
	}
	if !reflect.DeepEqual(posts, map[string]int{"CreateUser": 1, "SendLoginLink": 1}) {
		t.Errorf("c.do(http.MethodPost) calls: got %v", posts)
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

// checkHTTPUse resolves id through the type checker and judges it if it is
// something from net/http.
func checkHTTPUse(t *testing.T, fset *token.FileSet, info *types.Info, id *ast.Ident, owner string, inDo bool, writes map[string]int) {
	obj := info.Uses[id]
	if !isNetHTTP(obj) {
		return
	}
	pos := fset.Position(id.Pos())
	switch o := obj.(type) {
	case *types.Const:
		if httpWriteConsts[o.Name()] {
			writes[owner+":"+o.Name()]++
		}
	case *types.Var:
		if o.IsField() && o.Name() == "Method" {
			t.Errorf("%s: the Request.Method field is used; requests are built in do", pos)
		}
	case *types.Func:
		recv := recvName(o)
		switch {
		case recv == "" && (o.Name() == "NewRequest" || o.Name() == "NewRequestWithContext"):
			if !inDo {
				t.Errorf("%s: http.%s outside (*Client).do", pos, o.Name())
			}
		case recv == "" && (o.Name() == "Get" || o.Name() == "Post" || o.Name() == "PostForm" || o.Name() == "Head"):
			t.Errorf("%s: http.%s is a request shortcut", pos, o.Name())
		case recv == "Client" && o.Name() == "Do":
			if !inDo {
				t.Errorf("%s: (*http.Client).Do outside (*Client).do", pos)
			}
		case recv == "Client" && (o.Name() == "Get" || o.Name() == "Post" || o.Name() == "PostForm" || o.Name() == "Head"):
			t.Errorf("%s: (*http.Client).%s is a request shortcut", pos, o.Name())
		case o.Name() == "RoundTrip":
			t.Errorf("%s: RoundTrip called directly", pos)
		}
	}
}

func isPkgDoObj(o types.Object, pkg *types.Package) bool {
	f, ok := o.(*types.Func)
	return ok && f.Pkg() == pkg && f.Name() == "do" && recvName(f) == "Client"
}

func isPkgDo(info *types.Info, fun ast.Expr, pkg *types.Package) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return isPkgDoObj(info.Uses[f.Sel], pkg)
	case *ast.Ident:
		return isPkgDoObj(info.Uses[f], pkg)
	}
	return false
}

// checkDoCall requires the method argument of a c.do call to be a net/http
// Method constant written in place, GET everywhere except the two writes.
func checkDoCall(t *testing.T, fset *token.FileSet, info *types.Info, call *ast.CallExpr, owner string, posts map[string]int) {
	pos := fset.Position(call.Pos())
	if len(call.Args) < 2 {
		t.Errorf("%s: c.do arguments", pos)
		return
	}
	sel, ok := call.Args[1].(*ast.SelectorExpr)
	if !ok {
		t.Errorf("%s: c.do must be given an http.Method* constant written in place", pos)
		return
	}
	c, ok := info.Uses[sel.Sel].(*types.Const)
	if !ok || !isNetHTTP(c) || !strings.HasPrefix(c.Name(), "Method") {
		t.Errorf("%s: c.do must be given an http.Method* constant written in place", pos)
		return
	}
	switch c.Name() {
	case "MethodGet":
	case "MethodPost":
		posts[owner]++
	default:
		t.Errorf("%s: c.do with %s", pos, c.Name())
	}
}

// checkDoBody holds (*Client).do to passing its own, never reassigned, method
// parameter to http.NewRequestWithContext.
func checkDoBody(t *testing.T, fset *token.FileSet, info *types.Info, fn *ast.FuncDecl) {
	var param types.Object
	for _, f := range fn.Type.Params.List {
		for _, n := range f.Names {
			if n.Name == "method" {
				param = info.Defs[n]
			}
		}
	}
	if param == nil {
		t.Errorf("%s: do has no method parameter", fset.Position(fn.Pos()))
		return
	}
	refers := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && (info.Uses[id] == param || info.Defs[id] == param)
	}
	requests := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if refers(l) {
					t.Errorf("%s: do assigns to its method parameter", fset.Position(x.Pos()))
				}
			}
		case *ast.IncDecStmt:
			if refers(x.X) {
				t.Errorf("%s: do modifies its method parameter", fset.Position(x.Pos()))
			}
		case *ast.UnaryExpr:
			if x.Op == token.AND && refers(x.X) {
				t.Errorf("%s: do takes the address of its method parameter", fset.Position(x.Pos()))
			}
		case *ast.RangeStmt:
			if (x.Key != nil && refers(x.Key)) || (x.Value != nil && refers(x.Value)) {
				t.Errorf("%s: do ranges into its method parameter", fset.Position(x.Pos()))
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if f, ok := info.Uses[sel.Sel].(*types.Func); ok && isNetHTTP(f) && f.Name() == "NewRequestWithContext" {
					requests++
					if len(x.Args) < 2 || !refers(x.Args[1]) {
						t.Errorf("%s: do must pass its method parameter to NewRequestWithContext", fset.Position(x.Pos()))
					}
				}
			}
		}
		return true
	})
	if requests != 1 {
		t.Errorf("do must build exactly one request, found %d", requests)
	}
}

// checkEmailVerifiedAssign fails on any assignment to a field named
// EmailVerified; the only approved setting is the literal in CreateUser.
func checkEmailVerifiedAssign(t *testing.T, fset *token.FileSet, info *types.Info, lhs ast.Expr) {
	if sel, ok := lhs.(*ast.SelectorExpr); ok {
		if v, ok := info.Uses[sel.Sel].(*types.Var); ok && v.IsField() && v.Name() == "EmailVerified" {
			t.Errorf("%s: EmailVerified assigned outside the createUserRequest literal", fset.Position(lhs.Pos()))
		}
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
