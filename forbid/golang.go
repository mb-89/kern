package forbid

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// majorVersion is a Go import path's last element that names a major version, v2 and up.
var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// goUses answers what a Go file uses that a list may name: each import, each
// selector of an imported package (time.Now under any import name of time),
// and each string literal's commands and hosts.
func goUses(p string, src []byte, owners []Owner) ([]use, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("forbid: %s does not parse: %w", p, err)
	}
	line := func(n ast.Node) int { return fset.Position(n.Pos()).Line }
	calls := map[string]bool{}
	for _, e := range entries(owners, "calls") {
		calls[e] = true
	}
	var out []use
	named := map[string]string{} // the name a file reads a package by, to its import path
	for _, imp := range f.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, importUses(line(imp), ip, "/", owners)...)
		name := goName(ip)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		named[name] = ip
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok {
				if ip, ok := named[x.Name]; ok && calls[ip+"."+n.Sel.Name] {
					out = append(out, use{line(n), "call", ip + "." + n.Sel.Name})
				}
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if s, err := strconv.Unquote(n.Value); err == nil {
					out = append(out, stringUses(line(n), s, owners)...)
				}
			}
		}
		return true
	})
	return out, nil
}

// goName is the name a Go file reads an import path by where the import
// gives none, as far as the path tells it: its last element, the one before
// where that names a major version (golang.org/x/mod/v2 reads as mod), without
// a gopkg.in version (gopkg.in/yaml.v3 reads as yaml). A package named
// otherwise than its path is still caught by its import.
func goName(ip string) string {
	parts := strings.Split(ip, "/")
	last := parts[len(parts)-1]
	if majorVersion.MatchString(last) && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	if i := strings.Index(last, ".v"); i > 0 {
		last = last[:i] // gopkg.in/yaml.v3
	}
	return last
}
