// SPDX-License-Identifier: Apache-2.0

package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// DaemonSide are the packages (relative to the core module) where every
// external process (restic, ssh, docker, anything) must be started through
// core/internal/bounded, which gives each one a deadline and a clean stop
// (ADR 0012). The box agent is not among them: it starts the user's shells
// and runs restic under each call's own deadline.
var DaemonSide = []string{"sync", "local", "daemon", "driver", "cmd/portenvd", "cmd/portenv-runner", "cmd/portenv"}

// processStarts are the functions that start a process, by import path.
var processStarts = map[string][]string{
	"os/exec": {"Command", "CommandContext"},
	"os":      {"StartProcess"},
	"syscall": {"Exec", "ForkExec", "StartProcess"},
}

// CheckProcesses parses every Go file under the covered directories of root
// (skipping testdata and the allowed directories) and reports each place
// that starts a process directly: a call to, or any other use of,
// exec.Command, exec.CommandContext, os.StartProcess or syscall.Exec,
// ForkExec or StartProcess, and each exec.Cmd literal.
func CheckProcesses(root string, covered []string, allowed ...string) ([]Violation, error) {
	var out []Violation
	fset := token.NewFileSet()
	for _, dir := range covered {
		start := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(start); err != nil {
			return nil, err // a covered package that moved must be noticed
		}
		err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || slices.Contains(allowed, filepath.ToSlash(rel)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			out = append(out, checkProcessFile(fset, f)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func checkProcessFile(fset *token.FileSet, f *ast.File) []Violation {
	names := map[string]string{} // local name -> import path
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if _, ok := processStarts[path]; !ok {
			continue
		}
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = path
	}
	if len(names) == 0 {
		return nil
	}
	var out []Violation
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			pkg, ok := n.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, ok := names[pkg.Name]
			if ok && slices.Contains(processStarts[path], n.Sel.Name) {
				out = append(out, Violation{fset.Position(n.Pos()), "starts a process with " + pkg.Name + "." + n.Sel.Name + " outside core/internal/bounded (no deadline)"})
			}
		case *ast.CompositeLit:
			sel, ok := n.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && names[pkg.Name] == "os/exec" && sel.Sel.Name == "Cmd" {
				out = append(out, Violation{fset.Position(n.Pos()), "builds an exec.Cmd outside core/internal/bounded (no deadline)"})
			}
		}
		return true
	})
	return out
}
