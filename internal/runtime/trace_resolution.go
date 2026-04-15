package runtime

import (
	"path"
	"path/filepath"
	"strings"
)

func runtimeModuleFromEvent(event Event, dependency string) string {
	if module := runtimeModuleFromSpecifier(event.Module, dependency); module != "" {
		return module
	}
	if module := runtimeModuleFromResolvedPath(event.Resolved, dependency); module != "" {
		return module
	}
	return dependency
}

func runtimeModuleFromSpecifier(specifier, dependency string) string {
	specifier = strings.TrimSpace(specifier)
	if specifier == "" {
		return ""
	}
	if dependencyFromSpecifier(specifier) != dependency {
		return ""
	}
	return specifier
}

func runtimeModuleFromResolvedPath(value, dependency string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "file://")
	value = filepath.ToSlash(value)

	marker := "/node_modules/"
	pos := strings.LastIndex(value, marker)
	if pos < 0 {
		return ""
	}
	rest := value[pos+len(marker):]
	if rest == "" {
		return ""
	}
	parts := strings.Split(rest, "/")
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 2 {
			return ""
		}
		if dependency != parts[0]+"/"+parts[1] {
			return ""
		}
		if len(parts) == 2 {
			return dependency
		}
		return dependency + "/" + strings.Join(parts[2:], "/")
	}
	if dependency != parts[0] {
		return ""
	}
	if len(parts) == 1 {
		return dependency
	}
	return dependency + "/" + strings.Join(parts[1:], "/")
}

func runtimeSymbolFromModule(module, dependency string) string {
	if module == "" || dependency == "" {
		return ""
	}
	subpath := strings.TrimPrefix(module, dependency)
	subpath = strings.TrimPrefix(subpath, "/")
	if subpath == "" {
		return ""
	}
	base := path.Base(subpath)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" || name == "." {
		return ""
	}
	if name == "index" {
		dir := path.Base(path.Dir(subpath))
		if dir != "." && dir != "/" {
			return dir
		}
	}
	return name
}

func dependencyFromEvent(event Event) string {
	if dep := dependencyFromSpecifier(event.Module); dep != "" {
		return dep
	}
	return dependencyFromResolvedPath(event.Resolved)
}

func dependencyFromSpecifier(specifier string) string {
	specifier = strings.TrimSpace(specifier)
	if specifier == "" {
		return ""
	}
	if strings.HasPrefix(specifier, ".") || strings.HasPrefix(specifier, "/") || strings.Contains(specifier, ":") {
		return ""
	}
	if strings.HasPrefix(specifier, "@") {
		parts := strings.SplitN(specifier, "/", 3)
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	parts := strings.SplitN(specifier, "/", 2)
	return parts[0]
}

func dependencyFromResolvedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "file://")
	value = filepath.ToSlash(value)

	marker := "/node_modules/"
	pos := strings.LastIndex(value, marker)
	if pos < 0 {
		return ""
	}
	rest := value[pos+len(marker):]
	if rest == "" {
		return ""
	}
	parts := strings.Split(rest, "/")
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
