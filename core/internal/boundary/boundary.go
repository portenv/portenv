// SPDX-License-Identifier: Apache-2.0

// Package boundary enforces the box driver boundary: only packages under
// core/driver may import an engine SDK or run an engine CLI. Everything else
// reaches engines through the driver.Driver interface, and reaches the inside
// of a box through the box agent, never `docker exec`.
package boundary

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// engineImports are import path prefixes of engine SDKs.
var engineImports = []string{
	"github.com/docker/",
	"github.com/moby/",
	"github.com/containerd/",
	"github.com/fsouza/go-dockerclient",
	"github.com/firecracker-microvm/",
	"github.com/kata-containers/",
}

// engineCLIs are programs that drive an engine. "container" is Apple's CLI.
var engineCLIs = []string{
	"docker", "docker-compose", "nerdctl", "podman", "ctr", "crictl",
	"container", "firecracker", "kata-runtime",
}

// Violation is one place that crosses the boundary.
type Violation struct {
	Pos    token.Position
	Reason string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s", v.Pos, v.Reason)
}

// Check parses every Go file under root, skipping the directories named in
// allowed (relative to root) and testdata directories, and reports each
// engine SDK import and each os/exec call that runs an engine CLI.
func Check(root string, allowed ...string) ([]Violation, error) {
	var out []Violation
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "testdata" || slices.Contains(allowed, filepath.ToSlash(rel))) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		out = append(out, checkFile(fset, f)...)
		return nil
	})
	return out, err
}

func checkFile(fset *token.FileSet, f *ast.File) []Violation {
	var out []Violation
	execName := ""
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		for _, prefix := range engineImports {
			if strings.HasPrefix(path, prefix) {
				out = append(out, Violation{fset.Position(imp.Pos()), "imports engine SDK " + path + " outside core/driver"})
			}
		}
		if path == "os/exec" {
			execName = "exec"
			if imp.Name != nil {
				execName = imp.Name.Name
			}
		}
	}
	if execName == "" || execName == "_" {
		return out
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != execName {
			return true
		}
		argIdx := -1
		switch sel.Sel.Name {
		case "Command", "LookPath":
			argIdx = 0
		case "CommandContext":
			argIdx = 1
		}
		if argIdx < 0 || len(call.Args) <= argIdx {
			return true
		}
		lit, ok := call.Args[argIdx].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		prog, _ := strconv.Unquote(lit.Value)
		if slices.Contains(engineCLIs, filepath.Base(prog)) {
			out = append(out, Violation{fset.Position(call.Pos()), "runs engine CLI " + strconv.Quote(prog) + " outside core/driver"})
		}
		return true
	})
	return out
}
