package js

// nodeBuiltinModules contains the list of Node.js core/built-in modules.
// These modules should not be treated as npm dependencies.
//
// This list is based on Node.js's module.builtinModules and includes
// only the top-level module names (excluding private modules starting with '_'
// and subpath exports like 'fs/promises').
//
// To update this list, run:
//   node -p "[...require('module').builtinModules].filter(m => !m.startsWith('_') && !m.includes('/')).sort().join('\n')"
//
// Or programmatically in Node.js v16+:
//   const builtins = require('module').builtinModules
//     .filter(m => !m.startsWith('_') && !m.includes('/') && !m.startsWith('node:'))
//
// Last updated: 2026-02-11 (Node.js v24.x)
var nodeBuiltinModules = map[string]bool{
	"assert":             true,
	"async_hooks":        true,
	"buffer":             true,
	"child_process":      true,
	"cluster":            true,
	"console":            true,
	"constants":          true,
	"crypto":             true,
	"dgram":              true,
	"diagnostics_channel": true,
	"dns":                true,
	"domain":             true,
	"events":             true,
	"fs":                 true,
	"http":               true,
	"http2":              true,
	"https":              true,
	"inspector":          true,
	"module":             true,
	"net":                true,
	"os":                 true,
	"path":               true,
	"perf_hooks":         true,
	"process":            true,
	"punycode":           true,
	"querystring":        true,
	"readline":           true,
	"repl":               true,
	"stream":             true,
	"string_decoder":     true,
	"sys":                true,
	"timers":             true,
	"tls":                true,
	"trace_events":       true,
	"tty":                true,
	"url":                true,
	"util":               true,
	"v8":                 true,
	"vm":                 true,
	"wasi":               true,
	"worker_threads":     true,
	"zlib":               true,
}

// isNodeBuiltin checks if a module name is a Node.js built-in module.
// It handles both bare module names (e.g., "fs") and "node:" prefixed names (e.g., "node:fs").
func isNodeBuiltin(moduleName string) bool {
	// Strip "node:" prefix if present
	if len(moduleName) > 5 && moduleName[:5] == "node:" {
		moduleName = moduleName[5:]
	}
	
	// Check if it's a built-in module (including subpaths like "fs/promises")
	// We check the base module name before any "/"
	for i := 0; i < len(moduleName); i++ {
		if moduleName[i] == '/' {
			return nodeBuiltinModules[moduleName[:i]]
		}
	}
	
	return nodeBuiltinModules[moduleName]
}
