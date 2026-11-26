package js

import "strings"

const nodeBuiltinPrefix = "node:"

// nodeBuiltinModules contains the list of Node.js core/built-in modules.
// These modules should not be treated as npm dependencies.
//
// This list is based on Node.js's module.builtinModules and includes
// only the public module specifiers. Bare specifiers and node: specifiers have
// different supported sets, so subpaths are listed explicitly.
//
// To update both lists, run:
//
//	node -p "require('node:module').builtinModules.filter(name => !name.startsWith('_')).sort().join('\n')"
//
// Store the node:-only entries in nodePrefixedBuiltinModules without the prefix.
// Last checked against Node.js v26.13.1.
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
	if name, prefixed := strings.CutPrefix(moduleName, nodeBuiltinPrefix); prefixed {
		return nodeBuiltinModules[name] || nodePrefixedBuiltinModules[name]
	}
	return nodeBuiltinModules[moduleName]
}
