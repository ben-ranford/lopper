package js

import (
	"fmt"
	"strings"
)

type ExportSurface struct {
	Names            map[string]struct{}
	IncludesWildcard bool
	EntryPoints      []string
	Warnings         []string
}

type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	PackageManager       string            `json:"packageManager"`
	Workspaces           any               `json:"workspaces"`
	Main                 string            `json:"main"`
	Module               string            `json:"module"`
	Types                string            `json:"types"`
	Typings              string            `json:"typings"`
	Exports              any               `json:"exports"`
	License              any               `json:"license"`
	Licenses             []any             `json:"licenses"`
	Gypfile              bool              `json:"gypfile"`
	Scripts              map[string]string `json:"scripts"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Resolved             string            `json:"_resolved"`
	Integrity            string            `json:"_integrity"`
	PublishConfig        struct {
		Registry string `json:"registry"`
	} `json:"publishConfig"`
	Repository any `json:"repository"`
}

const (
	runtimeProfileNodeImport     = "node-import"
	runtimeProfileNodeRequire    = "node-require"
	runtimeProfileBrowserImport  = "browser-import"
	runtimeProfileBrowserRequire = "browser-require"
	defaultRuntimeProfile        = runtimeProfileNodeImport
)

const invalidDependencyFormat = "invalid dependency: %s"

type dependencyExportRequest struct {
	repoPath           string
	dependency         string
	dependencyRootPath string
	runtimeProfileName string
}

type runtimeProfile struct {
	name       string
	conditions []string
}

func supportedRuntimeProfiles() []string {
	return []string{
		runtimeProfileNodeImport,
		runtimeProfileNodeRequire,
		runtimeProfileBrowserImport,
		runtimeProfileBrowserRequire,
	}
}

func resolveRuntimeProfile(name string) (runtimeProfile, string) {
	trimmed := strings.TrimSpace(strings.ToLower(name))
	if trimmed == "" {
		trimmed = defaultRuntimeProfile
	}
	switch trimmed {
	case runtimeProfileNodeImport:
		return runtimeProfile{name: trimmed, conditions: []string{"node", "import", "default"}}, ""
	case runtimeProfileNodeRequire:
		return runtimeProfile{name: trimmed, conditions: []string{"node", "require", "default"}}, ""
	case runtimeProfileBrowserImport:
		return runtimeProfile{name: trimmed, conditions: []string{"browser", "import", "default"}}, ""
	case runtimeProfileBrowserRequire:
		return runtimeProfile{name: trimmed, conditions: []string{"browser", "require", "default"}}, ""
	default:
		return runtimeProfile{name: defaultRuntimeProfile, conditions: []string{"node", "import", "default"}}, fmt.Sprintf("unknown runtime profile %q; using %q (supported: %s)", name, defaultRuntimeProfile, strings.Join(supportedRuntimeProfiles(), ", "))
	}
}

func resolveDependencyExports(req dependencyExportRequest) (ExportSurface, error) {
	surface := ExportSurface{Names: make(map[string]struct{})}
	profile, profileWarning := resolveRuntimeProfile(req.runtimeProfileName)
	if profileWarning != "" {
		surface.Warnings = append(surface.Warnings, profileWarning)
	}
	depPath := req.dependencyRootPath
	if depPath == "" {
		root, err := dependencyRoot(req.repoPath, req.dependency)
		if err != nil {
			return surface, err
		}
		depPath = root
	}

	pkg, warnings, err := loadPackageJSONForSurface(depPath)
	if err != nil {
		surface.Warnings = append(surface.Warnings, warnings...)
		return surface, nil
	}
	surface.Warnings = append(surface.Warnings, warnings...)

	entrypoints := collectCandidateEntrypoints(pkg, profile, &surface)
	resolved := resolveEntrypoints(depPath, entrypoints, &surface)

	if len(resolved) == 0 {
		surface.Warnings = append(surface.Warnings, "no entrypoints resolved for dependency")
		return surface, nil
	}

	parseEntrypointsIntoSurface(depPath, resolved, &surface)

	return surface, nil
}
