package world

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Derive is pure, as docs/model-design.md has it: the package reads
// nothing but its inputs. This guard reads the package's own source and
// fails on an import or a call that reaches the system. It knows names,
// not types, so it guards against the easy slips, not against intent.

// impure are imports the package may not use.
var impure = map[string]bool{
	"os": true, "os/exec": true, "io/ioutil": true, "net": true, "net/http": true,
	"syscall": true, "math/rand": true, "math/rand/v2": true,
}

// clock are the time functions that read the clock or wait on it.
var clock = map[string]bool{
	"Now": true, "Since": true, "Until": true, "Sleep": true, "After": true,
	"AfterFunc": true, "Tick": true, "NewTimer": true, "NewTicker": true,
}

// pureCalls are the functions of overview's other packages the world may
// call, each pure. Their packages also hold functions that run programs
// and read files, so a new one is added here only once it is known pure.
var pureCalls = map[string]bool{
	"fleet.ShortDir":      true, // a directory as the view shows it
	"issues.Open":         true, // whether a status is open
	"issues.Rank":         true, // a status's place in the display order
	"issues.PriorityRank": true, // a priority's number
}

var ours = map[string]bool{"fleet": true, "issues": true, "review": true, "tmux": true}

func TestArchDeriveIsPure(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(at token.Pos, what string) {
		t.Errorf("world is pure: %s at %s (see docs/model-design.md, \"Enforcement\")", what, fset.Position(at))
	}
	for _, f := range pkgs["world"].Files {
		for _, im := range f.Imports {
			if p := strings.Trim(im.Path.Value, `"`); impure[p] {
				fail(im.Pos(), "an import of "+p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "time" && clock[n.Sel.Name] {
					fail(n.Pos(), "time."+n.Sel.Name+", which reads the clock")
				}
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "fleet" && n.Sel.Name == "Home" {
					fail(n.Pos(), "fleet.Home, whose methods read the config directory")
				}
			case *ast.CallExpr:
				s, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := s.X.(*ast.Ident); ok && ours[id.Name] && !pureCalls[id.Name+"."+s.Sel.Name] {
					fail(n.Pos(), "a call to "+id.Name+"."+s.Sel.Name+", not known to be pure")
				}
			}
			return true
		})
	}
}
