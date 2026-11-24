package js

// nodeBuiltinModules contains the list of Node.js core/built-in modules.
// These modules should not be treated as npm dependencies.
//
// This list is based on Node.js's module.builtinModules and includes
// only the public module specifiers. Bare specifiers and node: specifiers have
// different supported sets, so subpaths are listed explicitly.
//
// To update this list, run:
//
//	node -p "[...require('module').builtinModules].filter(m => !m.startsWith('_')).sort().join('\n')"
//
// Or programmatically in Node.js v16+:
//
//	const builtins = require('module').builtinModules
//	  .filter(m => !m.startsWith('_') && !m.startsWith('node:'))
//
// Last updated: 2026-02-11 (Node.js v24.x)
var nodeBuiltinModules = map[string]bool{
	"assert":              true,
	"assert/strict":       true,
	"async_hooks":         true,
	"buffer":              true,
	"child_process":       true,
	"cluster":             true,
	"console":             true,
	"constants":           true,
	"crypto":              true,
	"dgram":               true,
	"diagnostics_channel": true,
	"dns":                 true,
	"dns/promises":        true,
	"domain":              true,
	"events":              true,
	"fs":                  true,
	"fs/promises":         true,
	"http":                true,
	"http2":               true,
	"https":               true,
	"inspector":           true,
	"inspector/promises":  true,
	"module":              true,
	"net":                 true,
	"os":                  true,
	"path":                true,
	"path/posix":          true,
	"path/win32":          true,
	"perf_hooks":          true,
	"process":             true,
	"punycode":            true,
	"querystring":         true,
	"readline":            true,
	"readline/promises":   true,
	"repl":                true,
	"stream":              true,
	"stream/consumers":    true,
	"stream/promises":     true,
	"stream/web":          true,
	"string_decoder":      true,
	"sys":                 true,
	"timers":              true,
	"timers/promises":     true,
	"tls":                 true,
	"trace_events":        true,
	"tty":                 true,
	"url":                 true,
	"util":                true,
	"util/types":          true,
	"v8":                  true,
	"vm":                  true,
	"wasi":                true,
	"worker_threads":      true,
	"zlib":                true,
}

var nodePrefixedBuiltinModules = map[string]bool{
	"ffi":            true,
	"sea":            true,
	"sqlite":         true,
	"test":           true,
	"test/reporters": true,
}

// isNodeBuiltin checks if a module name is a Node.js built-in module.
// It matches Node's exact public specifiers for both bare names and node: names.
func isNodeBuiltin(moduleName string) bool {
	if len(moduleName) > 5 && moduleName[:5] == "node:" {
		moduleName = moduleName[5:]
		return nodeBuiltinModules[moduleName] || nodePrefixedBuiltinModules[moduleName]
	}
	return nodeBuiltinModules[moduleName]
}
