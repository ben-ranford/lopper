package js

import "testing"

const (
	testFsPromises  = "fs/promises"
	testBabelCore   = "@babel/core"
	testTypesNode   = "@types/node"
)

func TestIsNodeBuiltin(t *testing.T) {
	tests := []struct {
		name     string
		module   string
		expected bool
	}{
		// Core built-ins without prefix
		{"fs bare", "fs", true},
		{"path bare", "path", true},
		{"http bare", "http", true},
		{"crypto bare", "crypto", true},
		{"stream bare", "stream", true},

		// Built-ins with node: prefix
		{"fs with prefix", "node:fs", true},
		{"path with prefix", "node:path", true},
		{"http with prefix", "node:http", true},

		// Built-ins with subpaths
		{"fs/promises", testFsPromises, true},
		{"path/posix", "path/posix", true},
		{"node:fs/promises", "node:" + testFsPromises, true},

		// Not built-ins
		{"lodash", "lodash", false},
		{"react", "react", false},
		{"@babel/core", testBabelCore, false},
		{"fake-fs", "fake-fs", false},

		// Edge cases
		{"empty string", "", false},
		{"just node:", "node:", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNodeBuiltin(tt.module)
			if result != tt.expected {
				t.Errorf("isNodeBuiltin(%q) = %v, want %v", tt.module, result, tt.expected)
			}
		})
	}
}

func TestDependencyFromModuleBuiltins(t *testing.T) {
	tests := []struct {
		name     string
		module   string
		expected string
	}{
		// Node built-ins should return empty
		{"fs bare", "fs", ""},
		{"path bare", "path", ""},
		{"http bare", "http", ""},
		{"crypto bare", "crypto", ""},
		{"node:fs prefix", "node:fs", ""},
		{"node:path prefix", "node:path", ""},
		{"fs/promises subpath", testFsPromises, ""},

		// npm packages should return package name
		{"lodash", "lodash", "lodash"},
		{"lodash/map", "lodash/map", "lodash"},
		{"@babel/core", testBabelCore, testBabelCore},
		{"@types/node", testTypesNode, testTypesNode},

		// Relative/local imports should return empty
		{"./local", "./local", ""},
		{"../parent", "../parent", ""},
		{"/absolute", "/absolute", ""},

		// Edge cases
		{"empty", "", ""},
		{"whitespace", "  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dependencyFromModule(tt.module)
			if result != tt.expected {
				t.Errorf("dependencyFromModule(%q) = %q, want %q", tt.module, result, tt.expected)
			}
		})
	}
}
