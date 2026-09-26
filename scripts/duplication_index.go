//go:build ignore

// duplication_index supplies stable named-function identities to the dupl ratchet.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

type function struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Shape string `json:"shape"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var paths []string
	if err := json.NewDecoder(os.Stdin).Decode(&paths); err != nil {
		return err
	}
	functions := make([]function, 0)
	for _, path := range paths {
		indexed, err := indexFile(path)
		if err != nil {
			return err
		}
		functions = append(functions, indexed...)
	}
	return json.NewEncoder(os.Stdout).Encode(functions)
}

func indexFile(path string) ([]function, error) {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, path, nil, 0)
	if err != nil {
		return nil, err
	}
	functions := make([]function, 0, len(file.Decls))
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name, err := functionName(fn, path)
		if err != nil {
			return nil, err
		}
		functions = append(functions, function{
			Path: path, Name: name,
			Start: files.Position(fn.Pos()).Line, End: files.Position(fn.End()).Line,
			Shape: functionShape(fn),
		})
	}
	return functions, nil
}

func functionName(fn *ast.FuncDecl, path string) (string, error) {
	name := fn.Name.Name
	if fn.Recv == nil {
		return name, nil
	}
	receiver := fn.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	switch indexed := receiver.(type) {
	case *ast.IndexExpr:
		receiver = indexed.X
	case *ast.IndexListExpr:
		receiver = indexed.X
	}
	ident, ok := receiver.(*ast.Ident)
	if !ok {
		return "", fmt.Errorf("unsupported receiver in %s", path)
	}
	return ident.Name + "." + name, nil
}

func functionShape(fn *ast.FuncDecl) string {
	// Values and identifier spellings deliberately do not establish equivalence.
	// Nil markers preserve tree boundaries while positions/comments are omitted.
	var shape strings.Builder
	if fn.Recv != nil {
		writeNodeShape(&shape, fn.Recv)
	}
	writeNodeShape(&shape, fn.Type)
	writeNodeShape(&shape, fn.Body)
	digest := sha256.Sum256([]byte(shape.String()))
	return hex.EncodeToString(digest[:])
}

func writeNodeShape(shape *strings.Builder, node ast.Node) {
	ast.Inspect(node, func(node ast.Node) bool {
		fmt.Fprintf(shape, "%T;", node)
		return true
	})
}
