package pocketid

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/idp"
)

var allowedImports = map[string]bool{
	"bytes": true, "context": true, "encoding/json": true, "errors": true, "fmt": true,
	"io": true, "log/slog": true, "net/http": true, "net/url": true, "strings": true, "time": true,
	"github.com/paisans-software/paisans-account-service/internal/idp": true,
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

// The service is approved to make exactly four writes at the identity
// provider: create a user, send a sign-in link, update a user, where the
// update sends every field back as read and only SetDisabled, SetEmail and
// SetName may change a field, each its own, and set a user's groups, where
// ChangeGroups sends the list it has just read with only the requested
// additions and removals, after checkGroups. This test is type-checked, so a shadowed name or a look-alike
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
	pkg, err := conf.Check("github.com/paisans-software/paisans-account-service/internal/idp/pocketid", fset, parsed, info)
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
	exported := map[string]bool{"UsernameTaken": true, "EmailTaken": true, "FindUser": true, "AdminGroupEmails": true, "CreateUser": true, "SendLoginLink": true,
		"ListUsers": true, "GetUser": true, "SetDisabled": true, "SetEmail": true, "SetName": true,
		"ListGroups": true, "UserGroups": true, "ChangeGroups": true}
	puts := map[string]int{}
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
						checkDoCall(t, fset, info, x, owner, posts, puts)
					}
				case *ast.AssignStmt:
					for i, l := range x.Lhs {
						var rhs ast.Expr
						if len(x.Lhs) == len(x.Rhs) {
							rhs = x.Rhs[i]
						}
						checkPinnedFieldAssign(t, fset, info, l, owner, rhs, x.Tok == token.ASSIGN)
					}
				case *ast.IncDecStmt:
					checkPinnedFieldAssign(t, fset, info, x.X, owner, nil, false)
				case *ast.UnaryExpr:
					if x.Op == token.AND {
						checkPinnedFieldAssign(t, fset, info, x.X, owner, nil, false)
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
	if !reflect.DeepEqual(puts, map[string]int{"update": 1, "ChangeGroups": 1}) {
		t.Errorf("c.do(http.MethodPut) calls: got %v", puts)
	}
	want := map[string]int{"CreateUser:MethodPost": 1, "SendLoginLink:MethodPost": 1, "update:MethodPut": 1, "ChangeGroups:MethodPut": 1}
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
			case "update":
				checkUpdateFunc(t, fn)
			case "ChangeGroups":
				checkChangeGroupsFunc(t, fn)
			case "userGroups":
				call := onlyDoCall(t, fn)
				if sel, ok := call.Args[1].(*ast.SelectorExpr); !ok || sel.Sel.Name != "MethodGet" || !isUserPath(call.Args[2], paramName(fn, 1)) {
					t.Fatal(`userGroups must be one GET of "/api/users/" + url.PathEscape(id)`)
				}
			case "SendLoginLink":
				call := onlyDoCall(t, fn)
				if !isLoginPath(call.Args[2], paramName(fn, 1)) {
					t.Fatal(`SendLoginLink path must be "/api/users/" + url.PathEscape(userID) + "/one-time-access-email"`)
				}
				if !isLoginLinkLiteral(call.Args[3], recvIdent(fn), info) {
					t.Fatalf("SendLoginLink body must be %s", `loginLinkRequest{TTL: fmt.Sprintf("%dh", c.linkTTL/time.Hour)}`)
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
	fields = nil
	for i := 0; i < reflect.TypeOf(userUpdateRequest{}).NumField(); i++ {
		fields = append(fields, reflect.TypeOf(userUpdateRequest{}).Field(i).Tag.Get("json"))
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"disabled", "displayName", "email", "emailVerified", "firstName", "isAdmin", "lastName", "locale", "username"}) {
		t.Fatalf("userUpdateRequest fields changed: %v", fields)
	}
	gt := reflect.TypeOf(userGroupsRequest{})
	if gt.NumField() != 1 || gt.Field(0).Tag.Get("json") != "userGroupIds" {
		t.Fatalf("userGroupsRequest must have exactly one field, json \"userGroupIds\"")
	}
	groupLits := 0
	for _, file := range parsed {
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok {
				if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "userGroupsRequest" {
					groupLits++
				}
			}
			return true
		})
	}
	if groupLits != 1 {
		t.Fatalf("the package must build exactly one userGroupsRequest, in ChangeGroups; found %d", groupLits)
	}
	lt := reflect.TypeOf(loginLinkRequest{})
	if lt.NumField() != 1 || lt.Field(0).Tag.Get("json") != "ttl" {
		t.Fatalf("loginLinkRequest must have exactly one field, json \"ttl\"")
	}
}

// isLoginLinkLiteral reports whether e is
// loginLinkRequest{TTL: fmt.Sprintf("%dh", c.linkTTL/time.Hour)}, where c is
// the method's own receiver: the lifetime is the client's configured one,
// written in hours, and nothing else.
func isLoginLinkLiteral(e ast.Expr, recv *ast.Ident, info *types.Info) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "loginLinkRequest" || len(lit.Elts) != 1 {
		return false
	}
	kv, ok := lit.Elts[0].(*ast.KeyValueExpr)
	if !ok {
		return false
	}
	if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "TTL" {
		return false
	}
	call, ok := kv.Value.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 || !isPkgFunc(info, call.Fun, "fmt", "Sprintf") || !isStr(call.Args[0], "%dh") {
		return false
	}
	div, ok := call.Args[1].(*ast.BinaryExpr)
	if !ok || div.Op != token.QUO {
		return false
	}
	field, ok := div.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != "linkTTL" || recv == nil {
		return false
	}
	if v, ok := info.Uses[field.Sel].(*types.Var); !ok || !v.IsField() {
		return false
	}
	if x, ok := field.X.(*ast.Ident); !ok || info.Uses[x] == nil || info.Uses[x] != info.Defs[recv] {
		return false
	}
	hour, ok := div.Y.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	c, ok := info.Uses[hour.Sel].(*types.Const)
	return ok && c.Pkg() != nil && c.Pkg().Path() == "time" && c.Name() == "Hour"
}

// isPkgFunc reports whether fun is the function pkg.name, resolved by the
// type checker.
func isPkgFunc(info *types.Info, fun ast.Expr, pkg, name string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	f, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && f.Pkg() != nil && f.Pkg().Path() == pkg && f.Name() == name
}

// recvIdent is the name a method gives its receiver, or nil.
func recvIdent(fn *ast.FuncDecl) *ast.Ident {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 {
		return nil
	}
	return fn.Recv.List[0].Names[0]
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
// Method constant written in place, GET everywhere except the three writes.
func checkDoCall(t *testing.T, fset *token.FileSet, info *types.Info, call *ast.CallExpr, owner string, posts, puts map[string]int) {
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
	case "MethodPut":
		puts[owner]++
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

// updateChanges is which function may assign which userUpdateRequest field.
// Every other field goes back to Pocket ID as it was read.
var updateChanges = map[string]map[string]bool{
	"SetDisabled": {"Disabled": true},
	"SetEmail":    {"Email": true, "EmailVerified": true},
	"SetName":     {"DisplayName": true, "FirstName": true, "LastName": true},
}

// checkPinnedFieldAssign fails on any assignment to a field named
// EmailVerified, TTL or linkTTL outside its approved literal, except
// userUpdateRequest's own fields, which only the functions in updateChanges
// may assign, each only its own, and EmailVerified only the literal true.
func checkPinnedFieldAssign(t *testing.T, fset *token.FileSet, info *types.Info, lhs ast.Expr, owner string, rhs ast.Expr, plain bool) {
	sel, ok := lhs.(*ast.SelectorExpr)
	if !ok {
		return
	}
	v, ok := info.Uses[sel.Sel].(*types.Var)
	if !ok || !v.IsField() {
		return
	}
	if s, ok := info.Selections[sel]; ok && isUpdateRequest(s.Recv()) {
		if !plain || !updateChanges[owner][v.Name()] {
			t.Errorf("%s: userUpdateRequest.%s changed in %s", fset.Position(lhs.Pos()), v.Name(), owner)
			return
		}
		if v.Name() == "EmailVerified" {
			if id, ok := rhs.(*ast.Ident); !ok || id.Name != "true" {
				t.Errorf("%s: EmailVerified may only be set to true", fset.Position(lhs.Pos()))
			}
		}
		return
	}
	if v.Name() == "EmailVerified" || v.Name() == "TTL" || v.Name() == "linkTTL" || v.Name() == "UserGroupIDs" {
		t.Errorf("%s: %s assigned outside its approved literal", fset.Position(lhs.Pos()), v.Name())
	}
}

func isUpdateRequest(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Name() == "userUpdateRequest"
}

// checkUpdateFunc requires update to read the user, build the one
// userUpdateRequest from what it read, field for field, and write it with PUT
// to the same "/api/users/" + url.PathEscape(id) path.
func checkUpdateFunc(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	id := paramName(fn, 1)
	var calls []*ast.CallExpr
	var lits []*ast.CompositeLit
	ast.Inspect(fn, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "do" {
				calls = append(calls, x)
			}
		case *ast.CompositeLit:
			if tid, ok := x.Type.(*ast.Ident); ok && tid.Name == "userUpdateRequest" {
				lits = append(lits, x)
			}
		}
		return true
	})
	if len(calls) != 2 || len(lits) != 1 {
		t.Fatalf("update must make one read and one write and build one userUpdateRequest, found %d calls and %d literals", len(calls), len(lits))
	}
	for i, method := range []string{"MethodGet", "MethodPut"} {
		if sel, ok := calls[i].Args[1].(*ast.SelectorExpr); !ok || sel.Sel.Name != method {
			t.Fatalf("update's call %d must be %s", i+1, method)
		}
		if !isUserPath(calls[i].Args[2], id) {
			t.Fatalf(`update's %s path must be "/api/users/" + url.PathEscape(%s)`, method, id)
		}
	}
	read, ok := calls[0].Args[4].(*ast.UnaryExpr)
	if !ok || read.Op != token.AND {
		t.Fatal("update must read into a local value")
	}
	cur, ok := read.X.(*ast.Ident)
	if !ok {
		t.Fatal("update must read into a local value")
	}
	keys := map[string]bool{}
	for _, el := range lits[0].Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			t.Fatal("userUpdateRequest literal must use keyed fields")
		}
		key := kv.Key.(*ast.Ident).Name
		keys[key] = true
		v := kv.Value
		if c, ok := v.(*ast.CallExpr); ok && len(c.Args) == 1 {
			if f, ok := c.Fun.(*ast.Ident); ok && f.Name == "deref" {
				v = c.Args[0]
			}
		}
		if !fieldOf(v, cur.Name, key, false) {
			t.Fatalf("userUpdateRequest.%s must be %s.%s as read", key, cur.Name, key)
		}
	}
	if len(keys) != reflect.TypeOf(userUpdateRequest{}).NumField() {
		t.Fatalf("userUpdateRequest literal must set every field from the read, sets %v", keys)
	}
	if body, ok := calls[1].Args[3].(*ast.Ident); !ok || !bodyIsLit(fn, body.Name, lits[0]) {
		t.Fatal("update must write the userUpdateRequest it built")
	}
}

// bodyIsLit reports whether name is assigned once in fn, from lit.
func bodyIsLit(fn *ast.FuncDecl, name string, lit *ast.CompositeLit) bool {
	n, good := 0, false
	ast.Inspect(fn, func(x ast.Node) bool {
		as, ok := x.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			if lid, ok := l.(*ast.Ident); ok && lid.Name == name {
				n++
				good = len(as.Rhs) == len(as.Lhs) && as.Rhs[i] == lit
			}
		}
		return true
	})
	return n == 1 && good
}

func isUserPath(e ast.Expr, id string) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != token.ADD || !isStr(b.X, "/api/users/") {
		return false
	}
	call, ok := b.Y.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "PathEscape" {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	return ok && id != "" && arg.Name == id
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

// groupAPI is a Pocket ID holding the groups in all and one user, u1, in the
// groups whose IDs are in have. It answers the group listing, the user read
// and the group write, applying the write to have, and records every call.
// failReads makes every user read after the write fail.
type groupAPI struct {
	all       []map[string]any
	have      []string
	writes    int
	failReads bool
	calls     []call
}

func (g *groupAPI) serve(t *testing.T) *Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			c.Raw = string(b)
		}
		g.calls = append(g.calls, c)
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/user-groups":
			json.NewEncoder(w).Encode(map[string]any{"data": g.all, "pagination": map[string]any{"totalPages": 1}})
		case r.Method == "GET" && r.URL.Path == "/api/users/u1":
			if g.failReads && g.writes > 0 {
				w.WriteHeader(500)
				return
			}
			var mine []map[string]any
			for _, id := range g.have {
				for _, grp := range g.all {
					if grp["id"] == id {
						mine = append(mine, grp)
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "u1", "username": "bob", "userGroups": mine})
		case r.Method == "PUT" && r.URL.Path == "/api/users/u1/user-groups":
			var body struct {
				IDs []string `json:"userGroupIds"`
			}
			json.Unmarshal([]byte(c.Raw), &body)
			g.have = body.IDs
			g.writes++
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return mustNew(t, srv.URL, srv.Client(), week)
}

func groupsNamed(names ...string) []map[string]any {
	var out []map[string]any
	for _, n := range names {
		out = append(out, map[string]any{"id": "g-" + n, "name": n, "friendlyName": strings.ToUpper(n)})
	}
	return out
}

func names(gs []idp.Group) string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return strings.Join(out, ",")
}

// The group write sends exactly the list the fresh read found, with the
// requested additions and removals and nothing else, and never one that
// changes who is in operators. A failure is a question for the founder, not a
// test to fix.
func TestWriteSurfaceGroupBody(t *testing.T) {
	all := groupsNamed("members", "admins", "wiki", "blog", "operators")
	for _, c := range []struct {
		name              string
		have, add, remove []string
		body              string // the PUT body, or "" for no write
		err               error
		added, removed    string
	}{
		{"add", []string{"g-members"}, []string{"g-admins"}, nil, `{"userGroupIds":["g-members","g-admins"]}`, nil, "admins", ""},
		{"remove", []string{"g-members", "g-admins"}, nil, []string{"g-admins"}, `{"userGroupIds":["g-members"]}`, nil, "", "admins"},
		{"both", []string{"g-members", "g-admins"}, []string{"g-wiki"}, []string{"g-members"}, `{"userGroupIds":["g-admins","g-wiki"]}`, nil, "wiki", "members"},
		// wiki was added by someone else after the admin's page loaded: the
		// fresh read has it, and it is kept.
		{"concurrent", []string{"g-members", "g-wiki"}, []string{"g-blog"}, nil, `{"userGroupIds":["g-members","g-wiki","g-blog"]}`, nil, "blog", ""},
		// admins was removed by someone else first: removing it again is a no-op.
		{"already removed", []string{"g-members"}, []string{"g-wiki"}, []string{"g-admins"}, `{"userGroupIds":["g-members","g-wiki"]}`, nil, "wiki", ""},
		{"already added", []string{"g-members", "g-wiki"}, []string{"g-wiki", "g-wiki"}, nil, `{"userGroupIds":["g-members","g-wiki"]}`, nil, "", ""},
		{"operator keeps operators", []string{"g-operators", "g-admins"}, []string{"g-wiki"}, []string{"g-admins"}, `{"userGroupIds":["g-operators","g-wiki"]}`, nil, "wiki", "admins"},
		// An ID both added and removed is removed: nothing is added that was asked to go.
		{"added and removed", []string{"g-members", "g-wiki"}, []string{"g-wiki", "g-admins"}, []string{"g-wiki", "g-admins"}, `{"userGroupIds":["g-members"]}`, nil, "", "wiki"},
		{"remove operators", []string{"g-operators", "g-admins"}, nil, []string{"g-operators"}, "", idp.ErrOperatorsGroup, "", ""},
		{"add operators", []string{"g-members"}, []string{"g-operators"}, nil, "", idp.ErrOperatorsGroup, "", ""},
		{"add unknown", []string{"g-members"}, []string{"g-nope"}, nil, "", idp.ErrNoSuchGroup, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := &groupAPI{all: all, have: c.have}
			cl := api.serve(t)
			got, err := cl.ChangeGroups(context.Background(), "u1", c.add, c.remove)
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("error: %v, want %v", err, c.err)
			}
			var puts []call
			for _, k := range api.calls {
				if k.Method != "GET" {
					puts = append(puts, k)
				}
			}
			if c.body == "" {
				if len(puts) != 0 {
					t.Fatalf("wrote %+v", puts)
				}
				return
			}
			if len(puts) != 1 || puts[0].Method != "PUT" || puts[0].Raw != c.body {
				t.Fatalf("writes %+v, want one PUT %s", puts, c.body)
			}
			// The user is read immediately before the write and again after it.
			n := len(api.calls)
			if n < 3 || api.calls[n-3].Path != "/api/users/u1" || api.calls[n-1].Path != "/api/users/u1" || api.calls[n-2].Method != "PUT" {
				t.Fatalf("want a read, the write and a read, got %+v", api.calls)
			}
			if names(got.Added) != c.added || names(got.Removed) != c.removed {
				t.Fatalf("change: added %q removed %q", names(got.Added), names(got.Removed))
			}
		})
	}
}

// A write whose read-back fails reports what it sent, as unconfirmed.
func TestGroupWriteUnconfirmed(t *testing.T) {
	api := &groupAPI{all: groupsNamed("members", "admins"), have: []string{"g-members"}, failReads: true}
	got, err := api.serve(t).ChangeGroups(context.Background(), "u1", []string{"g-admins"}, []string{"g-members"})
	if !errors.Is(err, idp.ErrUnconfirmed) || api.writes != 1 || names(got.Added) != "admins" || names(got.Removed) != "members" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestListAndUserGroups(t *testing.T) {
	api := &groupAPI{all: groupsNamed("members", "admins"), have: []string{"g-admins"}}
	cl := api.serve(t)
	all, err := cl.ListGroups(context.Background())
	if err != nil || names(all) != "members,admins" || all[0].FriendlyName != "MEMBERS" || all[0].ID != "g-members" {
		t.Fatalf("%+v %v", all, err)
	}
	mine, err := cl.UserGroups(context.Background(), "u1")
	if err != nil || names(mine) != "admins" {
		t.Fatalf("%+v %v", mine, err)
	}
	if _, err := cl.UserGroups(context.Background(), "nobody"); !errors.Is(err, idp.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

// checkChangeGroupsFunc requires ChangeGroups to be, in this order and at the
// top level of its body: the group listing, all := c.listGroups(ctx); the
// fresh read, before := c.userGroups(ctx, id); next := groupList(
// groupIDs(before), add, remove); the refusal, if err := checkGroups(before,
// next, all); err != nil { return ... }; the one write, a PUT of
// userGroupsRequest{UserGroupIDs: next} to "/api/users/" + url.PathEscape(id)
// + "/user-groups"; and the read after it, c.userGroups(ctx, id). Each of
// all, before and next is assigned exactly once.
func checkChangeGroupsFunc(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	id, add, remove := paramName(fn, 1), paramName(fn, 2), paramName(fn, 3)
	assigned := map[string]int{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, l := range as.Lhs {
				if lid, ok := l.(*ast.Ident); ok {
					assigned[lid.Name]++
				}
			}
		}
		return true
	})
	for _, v := range []string{"all", "before", "next"} {
		if assigned[v] != 1 {
			t.Fatalf("ChangeGroups must assign %s exactly once, assigns it %d times", v, assigned[v])
		}
	}
	isCall := func(e ast.Expr, recv, name string, args ...func(ast.Expr) bool) bool {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) != len(args) {
			return false
		}
		switch f := c.Fun.(type) {
		case *ast.Ident:
			if recv != "" || f.Name != name {
				return false
			}
		case *ast.SelectorExpr:
			if x, ok := f.X.(*ast.Ident); !ok || x.Name != recv || f.Sel.Name != name {
				return false
			}
		default:
			return false
		}
		for i, a := range args {
			if !a(c.Args[i]) {
				return false
			}
		}
		return true
	}
	ident := func(name string) func(ast.Expr) bool {
		return func(e ast.Expr) bool { x, ok := e.(*ast.Ident); return ok && x.Name == name }
	}
	recv := recvIdent(fn).Name
	assignFrom := func(st ast.Stmt, v string, rhs func(ast.Expr) bool) bool {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			return false
		}
		lid, ok := as.Lhs[0].(*ast.Ident)
		return ok && lid.Name == v && rhs(as.Rhs[0])
	}
	readsUser := func(e ast.Expr) bool { return isCall(e, recv, "userGroups", ident("ctx"), ident(id)) }
	ifInit := func(st ast.Stmt, call func(ast.Expr) bool) bool {
		is, ok := st.(*ast.IfStmt)
		if !ok || is.Init == nil || len(is.Body.List) == 0 {
			return false
		}
		if _, ok := is.Body.List[len(is.Body.List)-1].(*ast.ReturnStmt); !ok {
			return false
		}
		as, ok := is.Init.(*ast.AssignStmt)
		return ok && len(as.Rhs) == 1 && call(as.Rhs[0])
	}
	isPut := func(e ast.Expr) bool {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) != 5 {
			return false
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "do" {
			return false
		}
		if m, ok := c.Args[1].(*ast.SelectorExpr); !ok || m.Sel.Name != "MethodPut" {
			return false
		}
		p, ok := c.Args[2].(*ast.BinaryExpr)
		if !ok || p.Op != token.ADD || !isStr(p.Y, "/user-groups") || !isUserPath(p.X, id) {
			return false
		}
		lit, ok := c.Args[3].(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 1 {
			return false
		}
		if tid, ok := lit.Type.(*ast.Ident); !ok || tid.Name != "userGroupsRequest" {
			return false
		}
		kv, ok := lit.Elts[0].(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "UserGroupIDs" {
			return false
		}
		return ident("next")(kv.Value) && ident("nil")(c.Args[4])
	}
	steps := []struct {
		what string
		ok   func(ast.Stmt) bool
	}{
		{"all, err := c.listGroups(ctx)", func(st ast.Stmt) bool {
			return assignFrom(st, "all", func(e ast.Expr) bool { return isCall(e, recv, "listGroups", ident("ctx")) })
		}},
		{"before, err := c.userGroups(ctx, id)", func(st ast.Stmt) bool { return assignFrom(st, "before", readsUser) }},
		{"next := groupList(groupIDs(before), add, remove)", func(st ast.Stmt) bool {
			return assignFrom(st, "next", func(e ast.Expr) bool {
				return isCall(e, "", "groupList", func(a ast.Expr) bool { return isCall(a, "", "groupIDs", ident("before")) }, ident(add), ident(remove))
			})
		}},
		{"if err := checkGroups(before, next, all); err != nil { return ... }", func(st ast.Stmt) bool {
			return ifInit(st, func(e ast.Expr) bool {
				return isCall(e, "", "checkGroups", ident("before"), ident("next"), ident("all"))
			})
		}},
		{`if err := c.do(ctx, http.MethodPut, "/api/users/"+url.PathEscape(id)+"/user-groups", userGroupsRequest{UserGroupIDs: next}, nil); err != nil { return ... }`, func(st ast.Stmt) bool {
			return ifInit(st, isPut)
		}},
		{"after, err := c.userGroups(ctx, id)", func(st ast.Stmt) bool { return assignFrom(st, "after", readsUser) }},
	}
	i := 0
	for _, st := range fn.Body.List {
		if i < len(steps) && steps[i].ok(st) {
			i++
		}
	}
	if i < len(steps) {
		t.Fatalf("ChangeGroups must do, in order at the top level of its body: %s", steps[i].what)
	}
	// Between the fresh read and the write, nothing else reads or writes.
	var between bool
	for _, st := range fn.Body.List {
		if steps[1].ok(st) {
			between = true
			continue
		}
		if steps[4].ok(st) {
			break
		}
		if between {
			ast.Inspect(st, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
						if x, ok := sel.X.(*ast.Ident); ok && x.Name == recv {
							t.Errorf("ChangeGroups calls %s.%s between the fresh read and the write", recv, sel.Sel.Name)
						}
					}
				}
				return true
			})
		}
	}
}
