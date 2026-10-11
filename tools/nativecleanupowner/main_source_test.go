package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Portable AST/source contracts are supporting checks, not native execution.
func TestSourceUsesOriginalOwnershipAPI(t *testing.T) {
	source, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := parser.ParseFile(token.NewFileSet(), "main_windows.go", source, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	start, cancellation := ownershipCallCounts(tree)
	if start != 1 || cancellation != 1 {
		t.Fatal("ownership must flow through the one existing start/cancellation site")
	}
	text := string(source)
	for _, required := range []string{"exec.CommandContext(context.Background()", "cmd.WaitDelay = commandAllowance", "cleanup()", "waits <- cmd.Wait()", "d.joinStreams(streams)", "win.SetHandleInformation(h, win.HANDLE_FLAG_INHERIT, 0)", "os.O_CREATE|os.O_EXCL", "case <-ctx.Done():"} {
		if !strings.Contains(text, required) {
			t.Fatal("missing source contract", required)
		}
	}
	for _, forbidden := range []string{"CreateJobObject(", "OpenJobObject(", "TerminateProcess(", "cmd.Cancel()", "GITHUB_OUTPUT", "CombinedOutput("} {
		if strings.Contains(text, forbidden) {
			t.Fatal("alternate owner/control source", forbidden)
		}
	}
}

func ownershipCallCounts(tree *ast.File) (int, int) {
	start, cancellation := 0, 0
	ast.Inspect(tree, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "StartCommand" {
			start++
		}
		if selector.Sel.Name == "ConfigureCommandCancellation" {
			cancellation++
		}
		return true
	})
	return start, cancellation
}

func TestJoinFileIsNotEarlyRecoveryAuthority(t *testing.T) {
	source, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := parser.ParseFile(token.NewFileSet(), "main_windows.go", source, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range tree.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "writeJoin" {
			continue
		}
		body := string(source[int(fn.Body.Pos())-1 : int(fn.Body.End())-1])
		if strings.Contains(body, "NewEncoder") {
			t.Fatal("join creation prematurely exposes authority before later helper joins")
		}
		return
	}
	t.Fatal("original join function missing")
}

func TestOwnerRepairsRetainAuthorityBoundaries(t *testing.T) {
	data, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{"d.retentionRequired", "r.CollectedExit = r.Joined && r.Err == nil && waitErr != nil", "auditTeardownAllowed(result)", "final guard exceeded original reserve", "original control write exceeded reserve", "d.cacheIdentity(true)", "d.cacheIdentity(false)"} {
		if !strings.Contains(source, required) {
			t.Fatal("missing repaired authority boundary", required)
		}
	}
	start := strings.Index(source, "cleanup, err := commandruntime.StartCommand(cmd)")
	budget := strings.Index(source[start:], "budget, launchErr := d.launchBudget()")
	if budget < 0 {
		t.Fatal("post-launch allowance not recomputed")
	}
	setup := strings.Index(source, "setup := d.ownedCommand")
	check := strings.Index(source[setup:], "d.loadAllocation")
	if !strings.Contains(source[setup:setup+check], "d.beginCleanup()") {
		t.Fatal("post-error verifier preceded cleanup clock")
	}
}
