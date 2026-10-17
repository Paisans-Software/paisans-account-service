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
	"strconv"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/idp"
)

const (
	selfPath = "github.com/paisans-software/paisans-account-service/internal/idp/pocketid"
	pidPath  = "github.com/paisans-software/pocketid-go"
)

var allowedImports = map[string]bool{
	"context": true, "errors": true, "fmt": true,
	"log/slog": true, "net/http": true, "net/netip": true, "net/url": true, "strings": true, "time": true,
	"github.com/paisans-software/paisans-account-service/internal/idp": true,
	pidPath: true,
}

// pidTypes are the shared client's types the package may name.
var pidTypes = map[string]bool{"Client": true, "User": true, "NewUser": true, "UserChange": true, "Group": true, "APIError": true}

// pidReads are the shared client's methods that only read; any function may
// call them.
var pidReads = map[string]bool{"Users": true, "User": true, "FindGroup": true, "Groups": true}

// pidWrites is each shared-client method that writes, and the functions that
// may call it, each exactly once.
var pidWrites = map[string][]string{
	"UpdateUser":     {"SetDisabled", "SetEmail", "SetName"},
	"CreateUser":     {"CreateUser"},
	"EmailLoginLink": {"SendLoginLink"},
	"SetUserGroups":  {"ChangeGroups"},
}

func isPidWrite(m string) bool { return pidWrites[m] != nil }

// userChanges is the UserChange each caller of UpdateUser sends: its fields,
// exactly, and the source of each, as "param.Field", "param", or "true" for a
// local set once to true. Disabled is ban and lift's; Email goes only with
// EmailVerified true, for an address change and a recovery; the names are the
// name change's. Every other field of the user goes back as read.
var userChanges = map[string]map[string]string{
	"SetDisabled": {"Disabled": "disabled"},
	"SetEmail":    {"Email": "email", "EmailVerified": "true"},
	"SetName":     {"DisplayName": "n.DisplayName", "FirstName": "n.FirstName", "LastName": "n.LastName"},
}

func isNetHTTP(o types.Object) bool {
	return o != nil && o.Pkg() != nil && o.Pkg().Path() == "net/http"
}

func isPid(o types.Object) bool {
	return o != nil && o.Pkg() != nil && o.Pkg().Path() == pidPath
}

// recvName is the bare type name of a method's receiver, or "" for a function.
func recvName(f *types.Func) string {
	r := f.Type().(*types.Signature).Recv()
	if r == nil {
		return ""
	}
	return namedName(r.Type())
}

// namedName is the bare name of a named type or a pointer to one, or "".
func namedName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return ""
}

// pidNamed is the name of t if it is one of the shared client's types, or a
// pointer to one.
func pidNamed(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok && isPid(n.Obj()) {
		return n.Obj().Name()
	}
	return ""
}

// selfNamed reports whether t is this package's Client, or a pointer to it.
func selfNamed(t types.Type, pkg *types.Package) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() == pkg && n.Obj().Name() == "Client"
}

// The service is approved to make exactly four writes at the identity
// provider: create a user, send a sign-in link, update a user, where
// pocketid-go's UpdateUser sends every field back as read except those its
// UserChange sets, and set a user's groups, where ChangeGroups sends the list
// it has just read with only the requested additions and removals, after
// checkGroups.
//
// Every request goes through the one shared client, pocketid-go, built once in
// New; the package builds and sends no request itself. Its write methods are
// named in pidWrites, each callable only from the functions named there, and
// each UserChange is pinned by userChanges, written in the call. This test is
// type-checked, so a shadowed name or a look-alike function cannot stand in
// for net/http or the shared client. It fails on another write, on a write
// moved to another function, on a UserChange field set by another caller or
// from another source, on any other shared-client method or any way of
// building or sending a request, on an import that could open a connection by
// another route, and on a body built from anything beyond the approved fields.
// TestWriteSurfaceOnTheWire checks the same from outside, at the server.
// A failure is a question for the founder, not a test to fix.
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
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check(selfPath, fset, parsed, info)
	if err != nil {
		t.Fatalf("type-checking the package: %v", err)
	}

	exported := map[string]bool{"UsernameTaken": true, "EmailTaken": true, "FindUser": true, "AdminGroupEmails": true, "CreateUser": true, "SendLoginLink": true,
		"ListUsers": true, "GetUser": true, "SetDisabled": true, "SetEmail": true, "SetName": true,
		"ListGroups": true, "UserGroups": true, "ChangeGroups": true}
	httpWrites := map[string]int{}
	pidCalls := map[string]int{}
	selfLits, pidClientLits, newUserLits := 0, 0, 0
	// changeLits are the UserChange literals written as the third argument
	// of a c.api.UpdateUser call; no other is allowed.
	changeLits := map[*ast.CompositeLit]bool{}
	for _, file := range parsed {
		for _, decl := range file.Decls {
			owner := "<package>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
				if fn.Name.IsExported() && !(fn.Recv == nil && fn.Name.Name == "New") && !(fn.Recv != nil && exported[fn.Name.Name]) {
					t.Errorf("%s: exported %s is not part of the approved surface", fset.Position(fn.Pos()), fn.Name.Name)
				}
			}
			// calledSel holds every selector that is the function of a call,
			// and okAPI every use of the api field that is the receiver of
			// one: a shared-client method may only be called, on c.api.
			calledSel := map[*ast.Ident]bool{}
			okAPI := map[*ast.Ident]bool{}
			litKeys := map[*ast.Ident]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						calledSel[sel.Sel] = true
						if inner, ok := sel.X.(*ast.SelectorExpr); ok {
							if v, ok := info.Uses[inner.Sel].(*types.Var); ok && v.IsField() && v.Name() == "api" && v.Pkg() == pkg {
								okAPI[inner.Sel] = true
								if sel.Sel.Name == "UpdateUser" && len(x.Args) == 3 {
									if lit, ok := x.Args[2].(*ast.CompositeLit); ok {
										changeLits[lit] = true
									}
								}
							}
						}
					}
				case *ast.CompositeLit:
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok {
								litKeys[k] = true
							}
						}
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					obj := info.Uses[x]
					if isNetHTTP(obj) {
						checkHTTPUse(t, fset, x, obj, owner, httpWrites)
					}
					if isPid(obj) {
						checkPidUse(t, fset, x, obj, owner, calledSel[x], pidCalls)
					}
					if v, ok := obj.(*types.Var); ok && v.IsField() && v.Pkg() == pkg && v.Name() == "api" && !okAPI[x] && !litKeys[x] {
						t.Errorf("%s: c.api used other than to call one of its methods", fset.Position(x.Pos()))
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING {
						if s, err := strconv.Unquote(x.Value); err == nil {
							switch strings.ToUpper(s) {
							case "POST", "PUT", "PATCH", "DELETE":
								t.Errorf("%s: write method spelled as a string literal", fset.Position(x.Pos()))
							}
						}
					}
				case *ast.CompositeLit:
					switch tv := info.Types[x].Type; {
					case selfNamed(tv, pkg):
						selfLits++
						if owner != "New" {
							t.Errorf("%s: a Client is built outside New", fset.Position(x.Pos()))
						}
						checkKeys(t, fset, x, "Client", "api", "linkTTL")
					case pidNamed(tv) == "Client":
						pidClientLits++
						if owner != "New" {
							t.Errorf("%s: a pocketid-go Client is built outside New", fset.Position(x.Pos()))
						}
						// No Transport: requests go straight to BaseURL.
						checkKeys(t, fset, x, "pocketid-go Client", "HTTP", "BaseURL", "APIKey", "Networks")
					case pidNamed(tv) == "NewUser":
						newUserLits++
					case pidNamed(tv) == "User":
						t.Errorf("%s: a pocketid-go User is built here; UpdateUser sends back the one it read", fset.Position(x.Pos()))
					case pidNamed(tv) == "UserChange":
						if !changeLits[x] {
							t.Errorf("%s: a UserChange is built other than in a c.api.UpdateUser call", fset.Position(x.Pos()))
						} else {
							checkUserChange(t, fset, info, owner, x)
						}
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
						if _, builtin := info.Uses[id].(*types.Builtin); builtin && pidNamed(info.Types[x.Args[0]].Type) != "" {
							t.Errorf("%s: new of a pocketid-go type", fset.Position(x.Pos()))
						}
					}
				case *ast.AssignStmt:
					for _, l := range x.Lhs {
						checkAssign(t, fset, info, pkg, l)
					}
				case *ast.IncDecStmt:
					checkAssign(t, fset, info, pkg, x.X)
				case *ast.UnaryExpr:
					if x.Op == token.AND {
						if sel, ok := x.X.(*ast.SelectorExpr); ok {
							if v, ok := info.Uses[sel.Sel].(*types.Var); ok && v.IsField() && (isPid(v) || v.Pkg() == pkg) {
								t.Errorf("%s: the address of field %s is taken", fset.Position(x.Pos()), v.Name())
							}
						}
					}
				}
				return true
			})
		}
	}
	if len(httpWrites) != 0 {
		t.Errorf("net/http write methods named: %v", httpWrites)
	}
	want := map[string]int{}
	for m, owners := range pidWrites {
		for _, owner := range owners {
			want[owner+":"+m] = 1
		}
	}
	if !reflect.DeepEqual(pidCalls, want) {
		t.Fatalf("write surface changed: got %v, want %v", pidCalls, want)
	}
	if len(changeLits) != len(userChanges) {
		t.Fatalf("want one UserChange literal in each of %d UpdateUser calls, got %d", len(userChanges), len(changeLits))
	}
	if selfLits != 1 || pidClientLits != 1 || newUserLits != 1 {
		t.Fatalf("want one Client, one pocketid-go Client and one NewUser literal, got %d, %d, %d", selfLits, pidClientLits, newUserLits)
	}

	for _, file := range parsed {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			switch fn.Name.Name {
			case "CreateUser":
				checkCreateUser(t, fn)
			case "SendLoginLink":
				checkSendLoginLink(t, fn, info)
			case "SetDisabled", "SetEmail", "SetName":
				checkUpdateCaller(t, fn, info)
			case "ChangeGroups":
				checkChangeGroupsFunc(t, fn)
			case "userGroups":
				calls := apiCalls(fn)
				if len(calls) != 1 || apiMethod(calls[0]) != "User" || !argsAre(calls[0], paramName(fn, 0), paramName(fn, 1)) {
					t.Fatal("userGroups must be one c.api.User(ctx, id)")
				}
			case "listGroups":
				calls := apiCalls(fn)
				if len(calls) != 1 || apiMethod(calls[0]) != "Groups" || !argsAre(calls[0], paramName(fn, 0)) {
					t.Fatal("listGroups must be one c.api.Groups(ctx)")
				}
			}
		}
	}
}

// checkKeys requires a composite literal to be keyed, with exactly keys.
func checkKeys(t *testing.T, fset *token.FileSet, lit *ast.CompositeLit, what string, keys ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	got := map[string]bool{}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			t.Errorf("%s: %s literal must use keyed fields", fset.Position(lit.Pos()), what)
			return
		}
		got[kv.Key.(*ast.Ident).Name] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: %s literal sets %v, want exactly %v", fset.Position(lit.Pos()), what, got, want)
	}
}

// checkHTTPUse judges a use of something from net/http.
// The package builds and sends no request itself.
func checkHTTPUse(t *testing.T, fset *token.FileSet, id *ast.Ident, obj types.Object, owner string, writes map[string]int) {
	pos := fset.Position(id.Pos())
	switch o := obj.(type) {
	case *types.Const:
		if strings.HasPrefix(o.Name(), "Method") && o.Name() != "MethodGet" {
			writes[owner+":"+o.Name()]++
		}
	case *types.Var:
		if o.IsField() && o.Name() == "Method" {
			t.Errorf("%s: the Request.Method field is used", pos)
		}
	case *types.Func:
		recv := recvName(o)
		switch {
		case recv == "" && (o.Name() == "NewRequestWithContext" || o.Name() == "NewRequest"):
			t.Errorf("%s: http.%s; every request goes through pocketid-go", pos, o.Name())
		case recv == "" && (o.Name() == "Get" || o.Name() == "Post" || o.Name() == "PostForm" || o.Name() == "Head"):
			t.Errorf("%s: http.%s is a request shortcut", pos, o.Name())
		case recv == "Client" && o.Name() == "Do":
			t.Errorf("%s: (*http.Client).Do; every request goes through pocketid-go", pos)
		case recv == "Client" && (o.Name() == "Get" || o.Name() == "Post" || o.Name() == "PostForm" || o.Name() == "Head"):
			t.Errorf("%s: (*http.Client).%s is a request shortcut", pos, o.Name())
		case o.Name() == "RoundTrip":
			t.Errorf("%s: RoundTrip called directly", pos)
		}
	}
}

// checkPidUse judges a use of something from the shared client: only the
// allowed types, Private, the read methods anywhere, and each write method
// from its one function, always called.
func checkPidUse(t *testing.T, fset *token.FileSet, id *ast.Ident, obj types.Object, owner string, called bool, calls map[string]int) {
	pos := fset.Position(id.Pos())
	switch o := obj.(type) {
	case *types.TypeName:
		if !pidTypes[o.Name()] {
			t.Errorf("%s: pocketid-go type %s is not on the allowlist", pos, o.Name())
		}
	case *types.Var:
		if !o.IsField() {
			t.Errorf("%s: pocketid-go variable %s is used", pos, o.Name())
		}
	case *types.Func:
		recv := recvName(o)
		switch {
		case recv == "" && o.Name() == "Private":
		case recv == "User" && o.Name() == "InGroup":
		case recv == "Client" && (pidReads[o.Name()] || isPidWrite(o.Name())):
			if !called {
				t.Errorf("%s: (*pocketid.Client).%s used other than by calling it", pos, o.Name())
			}
			if isPidWrite(o.Name()) {
				calls[owner+":"+o.Name()]++
			}
		default:
			t.Errorf("%s: pocketid-go %s.%s is not on the allowlist", pos, recv, o.Name())
		}
	default:
		t.Errorf("%s: pocketid-go %s is used", pos, obj.Name())
	}
}

// checkAssign fails on any assignment to a field of the package's own Client
// or of any of the shared client's types, and on any assignment through a
// pointer to one: the user UpdateUser sends is the one it read, changed only
// by the UserChange.
func checkAssign(t *testing.T, fset *token.FileSet, info *types.Info, pkg *types.Package, lhs ast.Expr) {
	pos := fset.Position(lhs.Pos())
	if star, ok := lhs.(*ast.StarExpr); ok {
		if pidNamed(info.Types[star].Type) != "" || selfNamed(info.Types[star].Type, pkg) {
			t.Errorf("%s: a shared-client value assigned through a pointer", pos)
		}
		return
	}
	sel, ok := lhs.(*ast.SelectorExpr)
	if !ok {
		return
	}
	v, ok := info.Uses[sel.Sel].(*types.Var)
	if !ok || !v.IsField() {
		return
	}
	s, ok := info.Selections[sel]
	if !ok {
		return
	}
	switch {
	case selfNamed(s.Recv(), pkg):
		t.Errorf("%s: Client.%s assigned outside its literal in New", pos, v.Name())
	case pidNamed(s.Recv()) != "":
		t.Errorf("%s: pocketid-go %s.%s assigned", pos, pidNamed(s.Recv()), v.Name())
	}
}

// apiCalls returns every c.api.M(...) call in fn, in source order.
func apiCalls(fn *ast.FuncDecl) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && apiMethod(c) != "" {
			out = append(out, c)
		}
		return true
	})
	return out
}

// apiMethod is M for a call x.api.M(...), or "".
func apiMethod(e ast.Expr) string {
	c, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "api" {
		return ""
	}
	return sel.Sel.Name
}

// argsAre reports whether call's arguments are exactly the named identifiers.
func argsAre(call *ast.CallExpr, names ...string) bool {
	if len(call.Args) != len(names) {
		return false
	}
	for i, a := range call.Args {
		if id, ok := a.(*ast.Ident); !ok || names[i] == "" || id.Name != names[i] {
			return false
		}
	}
	return true
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

// recvIdent is the name a method gives its receiver, or "".
func recvIdent(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func isStr(e ast.Expr, want string) bool {
	l, ok := e.(*ast.BasicLit)
	if !ok || l.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(l.Value)
	return err == nil && s == want
}

// isSel reports whether e is x.name for the identifier x.
func isSel(e ast.Expr, x, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && x != "" && id.Name == x
}

// checkCreateUser requires CreateUser to assign email once, from
// strings.TrimSpace(u.Email), and to make one call,
// c.api.CreateUser(ctx, pid.NewUser{Username: u.Username, Email: &email,
// DisplayName: u.DisplayName, FirstName: u.FirstName, LastName: u.LastName}):
// no admin flag, and the address unverified.
func checkCreateUser(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	ctx, u := paramName(fn, 0), paramName(fn, 1)
	calls := apiCalls(fn)
	if len(calls) != 1 || apiMethod(calls[0]) != "CreateUser" || len(calls[0].Args) != 2 {
		t.Fatal("CreateUser must make exactly one call, c.api.CreateUser(ctx, pid.NewUser{...})")
	}
	if id, ok := calls[0].Args[0].(*ast.Ident); !ok || id.Name != ctx {
		t.Fatal("CreateUser must pass its own context")
	}
	lit, ok := calls[0].Args[1].(*ast.CompositeLit)
	if !ok {
		t.Fatal("CreateUser's body must be a pid.NewUser literal written in the call")
	}
	keys := map[string]bool{}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			t.Fatal("the NewUser literal must use keyed fields")
		}
		key := kv.Key.(*ast.Ident).Name
		keys[key] = true
		good := false
		switch key {
		case "Username", "DisplayName", "FirstName", "LastName":
			good = isSel(kv.Value, u, key)
		case "Email":
			ue, ok := kv.Value.(*ast.UnaryExpr)
			if ok && ue.Op == token.AND {
				id, ok := ue.X.(*ast.Ident)
				good = ok && id.Name == "email"
			}
		}
		if !good {
			t.Fatalf("NewUser.%s is set from something other than the approved source", key)
		}
	}
	if len(keys) != 5 {
		t.Fatalf("the NewUser literal must set Username, Email, DisplayName, FirstName and LastName, sets %v", keys)
	}
	n := 0
	ast.Inspect(fn, func(x ast.Node) bool {
		as, ok := x.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			if id, ok := l.(*ast.Ident); ok && id.Name == "email" {
				n++
				c, ok := as.Rhs[i].(*ast.CallExpr)
				if len(as.Rhs) != len(as.Lhs) || !ok || len(c.Args) != 1 || !isSel(c.Fun, "strings", "TrimSpace") || !isSel(c.Args[0], u, "Email") {
					t.Fatal("email must be strings.TrimSpace(u.Email)")
				}
			}
		}
		return true
	})
	if n != 1 {
		t.Fatalf("CreateUser must assign email exactly once, assigns it %d times", n)
	}
}

// checkSendLoginLink requires SendLoginLink's one call to be
// c.api.EmailLoginLink(ctx, userID, c.linkTTL), where c is its receiver: the
// lifetime is the client's configured one, and nothing else.
func checkSendLoginLink(t *testing.T, fn *ast.FuncDecl, info *types.Info) {
	t.Helper()
	calls := apiCalls(fn)
	if len(calls) != 1 || apiMethod(calls[0]) != "EmailLoginLink" || len(calls[0].Args) != 3 {
		t.Fatal("SendLoginLink must make exactly one call, c.api.EmailLoginLink(ctx, userID, c.linkTTL)")
	}
	c := calls[0]
	for i, want := range []string{paramName(fn, 0), paramName(fn, 1)} {
		if id, ok := c.Args[i].(*ast.Ident); !ok || id.Name != want {
			t.Fatalf("SendLoginLink argument %d must be %s", i+1, want)
		}
	}
	if !isSel(c.Args[2], recvIdent(fn), "linkTTL") {
		t.Fatal("SendLoginLink's lifetime must be its receiver's linkTTL")
	}
	if v, ok := info.Uses[c.Args[2].(*ast.SelectorExpr).Sel].(*types.Var); !ok || !v.IsField() {
		t.Fatal("linkTTL must be the Client's field")
	}
}

// checkUpdateCaller requires SetDisabled, SetEmail and SetName each to make
// one call, c.api.UpdateUser(ctx, id, pid.UserChange{...}), with the
// function's own context and ID and the UserChange written in the call;
// checkUserChange judges the literal. Each variable the literal points into is
// assigned only as approved, and its address is taken only in the literal, so
// nothing can change what is sent after the literal is checked.
func checkUpdateCaller(t *testing.T, fn *ast.FuncDecl, info *types.Info) {
	t.Helper()
	name := fn.Name.Name
	calls := apiCalls(fn)
	if len(calls) != 1 || apiMethod(calls[0]) != "UpdateUser" || len(calls[0].Args) != 3 ||
		!isIdent(calls[0].Args[0], paramName(fn, 0)) || !isIdent(calls[0].Args[1], paramName(fn, 1)) {
		t.Fatalf("%s must make exactly one call, c.api.UpdateUser(ctx, id, pid.UserChange{...})", name)
	}
	if _, ok := calls[0].Args[2].(*ast.CompositeLit); !ok {
		t.Fatalf("%s's UserChange must be a literal written in the call", name)
	}
	param := paramName(fn, 2)
	// approved is how each variable the UserChange points into may be
	// assigned: the parameter only by SetEmail's trim, verified only once,
	// to true.
	approved := map[string]func(*ast.AssignStmt, int) bool{
		param: func(as *ast.AssignStmt, i int) bool {
			if name != "SetEmail" || as.Tok != token.ASSIGN || len(as.Rhs) != len(as.Lhs) {
				return false
			}
			c, ok := as.Rhs[i].(*ast.CallExpr)
			return ok && len(c.Args) == 1 && isSel(c.Fun, "strings", "TrimSpace") && isIdent(c.Args[0], param)
		},
		"verified": func(as *ast.AssignStmt, i int) bool {
			if name != "SetEmail" || as.Tok != token.DEFINE || len(as.Rhs) != len(as.Lhs) {
				return false
			}
			id, ok := as.Rhs[i].(*ast.Ident)
			return ok && id.Name == "true" && info.Uses[id] == types.Universe.Lookup("true")
		},
	}
	assigned := map[string]int{}
	addrs := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				root := rootIdent(l)
				if root == "" {
					continue
				}
				if ok := approved[root]; ok != nil {
					assigned[root]++
					if !ok(x, i) {
						t.Errorf("%s assigns %s other than as approved", name, root)
					}
				}
			}
		case *ast.IncDecStmt:
			if r := rootIdent(x.X); r == param || r == "verified" {
				t.Errorf("%s changes %s", name, r)
			}
		case *ast.UnaryExpr:
			if r := rootIdent(x.X); x.Op == token.AND && (r == param || r == "verified") {
				addrs++
			}
		}
		return true
	})
	if want := len(userChanges[name]); addrs != want {
		t.Fatalf("%s takes the address of %s or verified %d times; only the UserChange's %d fields may", name, param, addrs, want)
	}
	if name == "SetEmail" && (assigned[param] != 1 || assigned["verified"] != 1) {
		t.Fatalf("SetEmail must trim %s once and set verified once, to true", param)
	}
	if name != "SetEmail" && (assigned[param] != 0 || assigned["verified"] != 0) {
		t.Fatalf("%s must not assign %s", name, param)
	}
}

// checkUserChange requires a UserChange literal to set exactly the fields
// userChanges gives its function, each from its approved source: &param,
// &param.Field, or &verified for "true".
func checkUserChange(t *testing.T, fset *token.FileSet, info *types.Info, owner string, lit *ast.CompositeLit) {
	t.Helper()
	pos := fset.Position(lit.Pos())
	want, ok := userChanges[owner]
	if !ok {
		t.Errorf("%s: %s may not send a UserChange", pos, owner)
		return
	}
	got := map[string]bool{}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			t.Errorf("%s: the UserChange literal must use keyed fields", pos)
			return
		}
		key := kv.Key.(*ast.Ident).Name
		got[key] = true
		src, ok := want[key]
		if !ok {
			t.Errorf("%s: %s may not set UserChange.%s", pos, owner, key)
			continue
		}
		ue, ok := kv.Value.(*ast.UnaryExpr)
		if !ok || ue.Op != token.AND {
			t.Errorf("%s: UserChange.%s must be &%s", pos, key, src)
			continue
		}
		var good bool
		switch {
		case src == "true":
			good = isIdent(ue.X, "verified")
		case strings.Contains(src, "."):
			x, f, _ := strings.Cut(src, ".")
			good = isSel(ue.X, x, f)
		default:
			good = isIdent(ue.X, src)
		}
		if !good {
			t.Errorf("%s: UserChange.%s in %s must come from %s", pos, key, owner, src)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%s: %s's UserChange sets %v, want exactly the fields of %v", pos, owner, got, want)
	}
}

// rootIdent is the name x for an expression x, x.f, x.f.g or x[i], or "".
func rootIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return ""
		}
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && name != "" && id.Name == name
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
// next, all); err != nil { return ... }; the one write,
// if err := c.api.SetUserGroups(ctx, id, next); err != nil { return ... };
// and the read after it, c.userGroups(ctx, id). Each of all, before and next
// is assigned exactly once.
func checkChangeGroupsFunc(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	ctx, id, add, remove := paramName(fn, 0), paramName(fn, 1), paramName(fn, 2), paramName(fn, 3)
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
		return func(e ast.Expr) bool { return isIdent(e, name) }
	}
	recv := recvIdent(fn)
	assignFrom := func(st ast.Stmt, v string, rhs func(ast.Expr) bool) bool {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			return false
		}
		return isIdent(as.Lhs[0], v) && rhs(as.Rhs[0])
	}
	readsUser := func(e ast.Expr) bool { return isCall(e, recv, "userGroups", ident(ctx), ident(id)) }
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
	isWrite := func(e ast.Expr) bool {
		return apiMethod(e) == "SetUserGroups" && argsAre(e.(*ast.CallExpr), ctx, id, "next")
	}
	steps := []struct {
		what string
		ok   func(ast.Stmt) bool
	}{
		{"all, err := c.listGroups(ctx)", func(st ast.Stmt) bool {
			return assignFrom(st, "all", func(e ast.Expr) bool { return isCall(e, recv, "listGroups", ident(ctx)) })
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
		{"if err := c.api.SetUserGroups(ctx, id, next); err != nil { return ... }", func(st ast.Stmt) bool {
			return ifInit(st, isWrite)
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
					if apiMethod(c) != "" {
						t.Errorf("ChangeGroups calls c.api.%s between the fresh read and the write", apiMethod(c))
					}
				}
				return true
			})
		}
	}
}

// approvedRoutes are every request the service may make at Pocket ID, by
// method and route, with :id for a path segment. Groups is the GET of
// /api/user-groups; UpdateUser is a GET and a PUT of /api/users/:id.
var approvedRoutes = map[string]bool{
	"GET /api/users":                            true,
	"GET /api/users/:id":                        true,
	"GET /api/user-groups":                      true,
	"POST /api/users":                           true,
	"POST /api/users/:id/one-time-access-email": true,
	"PUT /api/users/:id":                        true,
	"PUT /api/users/:id/user-groups":            true,
}

// route is a request's method and path with its ID segment replaced by :id.
func route(method, path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 3 && parts[1] == "api" && (parts[2] == "users" || parts[2] == "user-groups") {
		parts[3] = ":id"
	}
	return method + " " + strings.Join(parts, "/")
}

// Every exported method, driven against a recording Pocket ID, makes exactly
// the requests listed for it, each approved and each with the key: one POST
// each for CreateUser and SendLoginLink, one PUT each for SetDisabled,
// SetEmail, SetName and ChangeGroups, and nothing but reads for the rest. This
// holds whatever the shared client does inside, so a new pocketid-go that
// sends something else fails here. A failure is a question for the founder,
// not a test to fix.
func TestWriteSurfaceOnTheWire(t *testing.T) {
	var calls []call
	user := map[string]any{"id": "u1", "username": "bob", "email": "bob@x.org", "displayName": "Bob", "firstName": "Bob",
		"isAdmin": false, "userGroups": []any{map[string]any{"id": "g-admins", "name": "admins"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{Method: r.Method, Path: r.URL.Path, Key: r.Header.Get("X-API-Key")})
		io.Copy(io.Discard, r.Body)
		switch route(r.Method, r.URL.Path) {
		case "GET /api/users":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{user}, "pagination": map[string]any{"totalPages": 1}})
		case "GET /api/user-groups":
			json.NewEncoder(w).Encode(map[string]any{"data": groupsNamed("members", "admins"), "pagination": map[string]any{"totalPages": 1}})
		case "GET /api/users/:id", "PUT /api/users/:id":
			json.NewEncoder(w).Encode(user)
		case "POST /api/users":
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(user)
		case "POST /api/users/:id/one-time-access-email":
			w.WriteHeader(204)
		case "PUT /api/users/:id/user-groups":
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c := mustNew(t, srv.URL, srv.Client(), week)
	ctx := context.Background()
	const (
		users   = "GET /api/users"
		user1   = "GET /api/users/:id"
		groups  = "GET /api/user-groups"
		update  = user1 + ";PUT /api/users/:id"
		setGrps = "PUT /api/users/:id/user-groups"
	)
	for _, m := range []struct {
		name     string
		requests string
		run      func() error
	}{
		{"UsernameTaken", users, func() error { _, err := c.UsernameTaken(ctx, "bob"); return err }},
		{"EmailTaken", users, func() error { _, err := c.EmailTaken(ctx, "bob@x.org"); return err }},
		{"FindUser", users, func() error { _, _, err := c.FindUser(ctx, "bob"); return err }},
		{"AdminGroupEmails", groups + ";" + users, func() error { _, err := c.AdminGroupEmails(ctx, "admins"); return err }},
		{"ListUsers", users, func() error { _, err := c.ListUsers(ctx); return err }},
		{"GetUser", user1, func() error { _, err := c.GetUser(ctx, "u1"); return err }},
		{"ListGroups", groups, func() error { _, err := c.ListGroups(ctx); return err }},
		{"UserGroups", user1, func() error { _, err := c.UserGroups(ctx, "u1"); return err }},
		{"CreateUser", "POST /api/users", func() error {
			_, err := c.CreateUser(ctx, idp.NewUser{Username: "ann", Email: "ann@x.org", DisplayName: "Ann"})
			return err
		}},
		{"SendLoginLink", "POST /api/users/:id/one-time-access-email", func() error { return c.SendLoginLink(ctx, "u1") }},
		{"SetDisabled", update, func() error { return c.SetDisabled(ctx, "u1", true) }},
		{"SetEmail", update, func() error { return c.SetEmail(ctx, "u1", "new@x.org") }},
		{"SetName", update, func() error { return c.SetName(ctx, "u1", idp.Name{DisplayName: "B"}) }},
		{"ChangeGroups", groups + ";" + user1 + ";" + setGrps + ";" + user1, func() error {
			_, err := c.ChangeGroups(ctx, "u1", []string{"g-members"}, nil)
			return err
		}},
	} {
		calls = nil
		if err := m.run(); err != nil {
			t.Errorf("%s: %v", m.name, err)
			continue
		}
		var got []string
		for _, k := range calls {
			r := route(k.Method, k.Path)
			if !approvedRoutes[r] {
				t.Errorf("%s: request %s is not approved", m.name, r)
			}
			if k.Key != "KEY" {
				t.Errorf("%s: %s without the key", m.name, r)
			}
			got = append(got, r)
		}
		if strings.Join(got, ";") != m.requests {
			t.Errorf("%s: requests %v, want %q", m.name, got, m.requests)
		}
	}
}
