package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"
)

// The architecture test reads the package's own source and fails when
// code steps around the control flow docs/model-design.md lays down:
// sources land in world.Inputs only through setInputs, world.Derive is
// the only join, presentation reads the World and the UI state, and
// nothing Update, View or Init runs calls out to the system except
// through a Cmd. It is a guard on names, not a type checker: it knows the
// model by its fields and methods, so a store copied into a local first
// gets past it. Each allowlist entry says why the function may do what
// the rule otherwise forbids. A presentation function belongs in none of
// them.

// deriveAllowed may name world.Derive or call deps.derive.
var deriveAllowed = map[string]string{
	"model.world": "the one memoized derivation, once per generation of the inputs",
	"systemDeps":  "wires world.Derive in as deps.derive, which only model.world calls",
}

// inputsWriters may assign to the model's inputs.
var inputsWriters = map[string]string{
	"model.setInputs": "the one place the inputs change, taking a new generation",
}

// sourceReaders may read the inputs (m.in) or the source caches and
// in-flight bookkeeping beside them. Everything else reads the World.
var sourceReaders = map[string]string{
	"model.setInputs":      "composes the inputs from the caches",
	"model.world":          "derives the World from the inputs",
	"model.landRows":       "lands a refresh: the rows' roots",
	"model.setPanes":       "lands a pane listing: the panes' roots",
	"model.dropTrackers":   "drops the trackers of projects that left the World, as a refresh lands",
	"model.landTracker":    "lands a tracker read and clears it from the reads in flight",
	"model.readTrackers":   "effect: asks for the trackers no read is out for",
	"model.readIssue":      "effect: an issue's details are due again when its .issues stamp moves",
	"model.trackerReading": "bookkeeping: whether a tracker read is out, for the picker's \"reading…\"",
	"model.pane":           "tmux layout lookup for the stage, the zoom and the frames",
	"model.holder":         "tmux layout lookup: the holder pane",
	"model.inStage":        "tmux layout: the panes beside the list, for the zoom and its width",
	"model.restage":        "effect: plans the list's move between tmux windows",
	"model.frames":         "effect: the panes of the view's session whose rules to set",
	"model.openReview":     "effect: finds a session's review pane, or plans its split",
	"model.closeTerminal":  "effect: plans closing a window from the panes listed now",
	"model.stageUpdate":    "lands tmux's answers: frames set, windows closed",
	"model.stageLanded":    "lands a move's answer, keeping where the list went",
}

// sourceStores are the model's fields that hold sources outside the
// World: the inputs and the caches setInputs composes them from.
var sourceStores = map[string]bool{"in": true, "trackers": true, "reading": true, "rowRoots": true, "paneRoots": true}

// effectRoots are where the runtime calls into the model. Nothing they
// reach may call out to the system directly.
var effectRoots = []string{"model.Update", "model.View", "model.Init"}

// effectfulDeps are the model's fields whose calls reach the system.
var effectfulDeps = map[string]bool{"deps": true, "mux": true, "home": true, "issues": true}

// effectAllowed are calls to those fields that may run outside a Cmd, by
// the function and the call.
var effectAllowed = map[string]string{
	"model.Update deps.now":   "reads the clock once, at the door, into m.now",
	"* home.Name":             "pure: the config directory's name",
	"* home.ClaudeEnv":        "pure: the environment a pane's claude joins this home with",
	"* home.ReviewsDir":       "pure: a path under the config directory",
	"* home.AttachArgs":       "pure: builds a command line, looks nothing up",
	"* home.NewArgs":          "pure: builds a command line, looks nothing up",
	"model.world deps.derive": "world.Derive, which is pure",
}

const archDoc = "see docs/model-design.md, \"Enforcement\""

// archFunc is a function of the package as the test reads it.
type archFunc struct {
	name string
	decl *ast.FuncDecl
}

// imports are the names the package's files import packages under.
var imports = map[string]bool{}

func archSource(t *testing.T) (*token.FileSet, []archFunc) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []archFunc
	for _, name := range slices.Sorted(maps.Keys(pkgs[pkgName].Files)) {
		file := pkgs[pkgName].Files[name]
		for _, im := range file.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			n := path[strings.LastIndex(path, "/")+1:]
			if im.Name != nil {
				n = im.Name.Name
			}
			imports[n] = true
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				out = append(out, archFunc{funcName(fd), fd})
			}
		}
	}
	return fset, out
}

const pkgName = "tui"

// funcName is "model.apply" for a method and "wrap" for a function.
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// field is the model field x selects, as in m.in or m.in.Rows, and
// whether x is one: a selector on a plain identifier.
func field(x ast.Expr) (string, bool) {
	for {
		s, ok := x.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		if _, ok := s.X.(*ast.Ident); ok {
			return s.Sel.Name, true
		}
		x = s.X
	}
}

func archFail(t *testing.T, fset *token.FileSet, rule, fn string, at token.Pos, what string) {
	t.Helper()
	t.Errorf("%s: %s in %s at %s (%s)", rule, what, fn, fset.Position(at), archDoc)
}

// Only the memoized derivation joins the sources.
func TestArchDeriveRunsInOnePlace(t *testing.T) {
	fset, fns := archSource(t)
	for _, f := range fns {
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			s, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, _ := s.X.(*ast.Ident)
			named := pkg != nil && pkg.Name == "world" && s.Sel.Name == "Derive"
			if dep, ok := field(s.X); ok && dep == "deps" && s.Sel.Name == "derive" {
				named = true
			}
			if _, ok := deriveAllowed[f.name]; named && !ok {
				archFail(t, fset, "world.Derive is the only join and runs only in model.world", f.name, s.Pos(), "a derivation")
			}
			return true
		})
	}
}

// The inputs change only in setInputs.
func TestArchInputsChangeOnlyInSetInputs(t *testing.T) {
	fset, fns := archSource(t)
	for _, f := range fns {
		if _, ok := inputsWriters[f.name]; ok {
			continue
		}
		check := func(x ast.Expr) {
			for {
				switch e := x.(type) {
				case *ast.IndexExpr:
					x = e.X
					continue
				case *ast.ParenExpr:
					x = e.X
					continue
				case *ast.StarExpr:
					x = e.X
					continue
				}
				break
			}
			for s, ok := x.(*ast.SelectorExpr); ok; s, ok = s.X.(*ast.SelectorExpr) {
				if _, id := s.X.(*ast.Ident); id && s.Sel.Name == "in" {
					archFail(t, fset, "the inputs change only in setInputs", f.name, x.Pos(), "a write to the inputs")
				}
			}
		}
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for _, l := range n.Lhs {
					check(l)
				}
			case *ast.IncDecStmt:
				check(n.X)
			case *ast.UnaryExpr:
				if n.Op == token.AND {
					check(n.X)
				}
			}
			return true
		})
	}
}

// The sources are read only where they land, where they are composed
// into the inputs, and where effects are planned. Presentation reads the
// World.
func TestArchSourcesAreReadOnlyWhereTheyLand(t *testing.T) {
	fset, fns := archSource(t)
	reads := map[string]bool{}
	for _, f := range fns {
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			s, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, id := s.X.(*ast.Ident); !id || !sourceStores[s.Sel.Name] {
				return true
			}
			reads[f.name] = true
			if _, ok := sourceReaders[f.name]; !ok {
				archFail(t, fset, "sources are read only where they land or effects are planned; views read the World", f.name, s.Pos(), "a read of m."+s.Sel.Name)
			}
			return true
		})
	}
	// An entry for a function that no longer reads a source goes, so the
	// list stays the list of real exceptions.
	for _, name := range slices.Sorted(maps.Keys(sourceReaders)) {
		if !reads[name] {
			t.Errorf("sourceReaders allows %s, which reads no source: drop it", name)
		}
	}
}

// Nothing Update, View or Init reaches calls out to the system outside a
// Cmd: apply plans, and the runtime runs what it planned. A call out is a
// call to one of the model's effectful fields, directly (m.mux.Panes())
// or through a local taken from one (mux := m.mux; mux.Panes()), or to a
// function of the standard library that reads the clock, the disk or
// runs a program.
func TestArchEffectsRunOnlyInCmds(t *testing.T) {
	fset, fns := archSource(t)
	byName := map[string][]*ast.FuncDecl{}
	methods := map[string][]string{} // method name → its full names
	for _, f := range fns {
		byName[f.name] = append(byName[f.name], f.decl)
		if f.decl.Recv != nil {
			methods[f.decl.Name.Name] = append(methods[f.decl.Name.Name], f.name)
		}
	}
	for b := range cmdBuilders {
		ds := byName[b]
		if len(ds) != 1 || !returnsCmd(ds[0].Type) {
			t.Errorf("cmdBuilders names %s, which is not one function returning only a tea.Cmd", b)
		}
	}
	seen := map[string]bool{}
	queue := slices.Clone(effectRoots)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, d := range byName[name] {
			aliases := depAliases(d)
			walkNow(d.Body, func(call *ast.CallExpr) {
				// An effectful field handed to a function that runs now
				// is a call out too, unless the function only builds a Cmd.
				callee := ""
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					callee = fn.Name
				case *ast.SelectorExpr:
					callee = fn.Sel.Name
				}
				for _, arg := range call.Args {
					if dep, ok := depOf(arg, aliases); ok {
						if _, ok := cmdBuilders[callee]; !ok {
							archFail(t, fset, "Update, View and Init call out to the system only inside a Cmd", name, arg.Pos(), "m."+dep+" passed to "+callee+", which runs now")
						}
					}
				}
				var fun ast.Expr = call.Fun
				if ix, ok := fun.(*ast.IndexExpr); ok {
					fun = ix.X // a generic call
				}
				what := ""
				switch fn := fun.(type) {
				case *ast.Ident:
					if dep, ok := aliases[fn.Name]; ok {
						what = "m." + dep // run := m.issues; run(…)
						break
					}
					queue = append(queue, fn.Name)
				case *ast.SelectorExpr:
					if dep, ok := depOf(fn.X, aliases); ok {
						what = "m." + dep + "." + fn.Sel.Name // m.mux.Panes(), mux.Panes()
						break
					}
					if dep, ok := depOf(fn, aliases); ok {
						what = "m." + dep // m.issues(…), m.deps.now()
						break
					}
					if id, ok := fn.X.(*ast.Ident); ok && imports[id.Name] {
						if system[id.Name+"."+fn.Sel.Name] || system[id.Name+".*"] {
							what = id.Name + "." + fn.Sel.Name
						}
						break // another package's function
					}
					// A method: any of the package's with the name, as
					// the test knows no types.
					queue = append(queue, methods[fn.Sel.Name]...)
				}
				if what == "" {
					return
				}
				key := strings.TrimPrefix(what, "m.")
				_, any := effectAllowed["* "+key]
				_, here := effectAllowed[name+" "+key]
				if !any && !here {
					archFail(t, fset, "Update, View and Init call out to the system only inside a Cmd", name, call.Pos(), "a call to "+what)
				}
			})
		}
	}
	if !seen["model.apply"] || !seen["model.footer"] || !seen["model.settle"] || !seen["model.keyActions"] || !seen["model.effects"] {
		t.Fatal("the walk from Update and View never reached apply, footer, settle, keyActions or effects: the test has lost the model's shape")
	}
}

// cmdBuilders take an effectful field and only build a Cmd around it,
// which the runtime runs later, by name. Each must return a tea.Cmd and
// nothing else, which the test checks.
var cmdBuilders = map[string]string{
	"listPanes":    "lists tmux's panes in its Cmd",
	"stageCmd":     "moves the list in its Cmd",
	"drawCmd":      "writes the holder's tty in its Cmd",
	"changesCmd":   "runs git or jj in its Cmd",
	"pinCmd":       "reads and writes the pins in its Cmd",
	"spawnCmd":     "starts a pane in its Cmd",
	"zoomCmd":      "zooms the list in its Cmd",
	"closeCmd":     "closes a window in its Cmd",
	"readPathsCmd": "reads directories in its Cmd",
}

// system are the standard library's functions that read the clock or
// the disk or run a program, by package and name; ".*" is the whole
// package.
var system = map[string]bool{
	"time.Now": true, "time.Since": true, "time.Until": true, "time.Sleep": true,
	"os.*": true, "exec.*": true, "ioutil.*": true, "user.*": true,
	"filepath.Glob": true, "filepath.EvalSymlinks": true, "filepath.Abs": true,
	"filepath.Walk": true, "filepath.WalkDir": true,
}

// depOf says which effectful field x is: m.mux gives "mux", m.deps.now
// gives "deps.now", and a local in aliases gives what it was taken from.
// The model is known by name: a receiver or parameter of type model,
// which aliases holds under modelKey.
func depOf(x ast.Expr, aliases map[string]string) (string, bool) {
	switch x := x.(type) {
	case *ast.Ident:
		dep, ok := aliases[x.Name]
		return dep, ok && dep != modelKey
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && effectfulDeps[x.Sel.Name] && aliases[id.Name] == modelKey {
			return x.Sel.Name, true
		}
		if in, ok := depOf(x.X, aliases); ok && !strings.Contains(in, ".") {
			return in + "." + x.Sel.Name, true
		}
	}
	return "", false
}

// modelKey marks a name in aliases that is the model itself.
const modelKey = "model"

// depAliases are the names in fd that stand for the model, a receiver or
// parameter of type model, and the locals taken from an effectful field
// of it, as mux := m.mux or read := m.deps.recentTurns. It keeps one map
// for the whole function and ignores scope, so a later local that reuses
// an alias's name is still taken for the alias. That errs towards a
// false failure, which renaming the local fixes, never a missed one.
func depAliases(fd *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	models := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			if id, ok := f.Type.(*ast.Ident); ok && id.Name == "model" {
				for _, n := range f.Names {
					out[n.Name] = modelKey
				}
			}
		}
	}
	models(fd.Recv)
	models(fd.Type.Params)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			models(fl.Type.Params)
		}
		return true
	})
	bind := func(lhs ast.Expr, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		if !ok {
			return
		}
		if dep, ok := depOf(rhs, out); ok {
			out[id.Name] = dep
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i := range n.Lhs {
					bind(n.Lhs[i], n.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i := range n.Names {
					bind(n.Names[i], n.Values[i])
				}
			}
		}
		return true
	})
	return out
}

// walkNow calls visit for every call body runs now: it leaves out the
// bodies of func literals that return a tea.Msg, which are Cmds or
// callbacks such as tea.Tick's that the runtime runs later, and follows
// every other literal, as those run in place.
func walkNow(body ast.Node, visit func(*ast.CallExpr)) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return !returnsMsg(n.Type)
		case *ast.CallExpr:
			visit(n)
		}
		return true
	})
}

// returnsCmd says whether a func type's one result is a tea.Cmd.
func returnsCmd(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	s, ok := ft.Results.List[0].Type.(*ast.SelectorExpr)
	return ok && fmt.Sprint(s.X) == "tea" && s.Sel.Name == "Cmd"
}

// returnsMsg says whether a func type's one result is a tea.Msg.
func returnsMsg(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	s, ok := ft.Results.List[0].Type.(*ast.SelectorExpr)
	return ok && fmt.Sprint(s.X) == "tea" && s.Sel.Name == "Msg"
}
