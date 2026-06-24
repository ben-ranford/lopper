package runtime

import (
	"path"
	"path/filepath"
	"strings"
)

const (
	runtimeLanguageJSTS   = "js-ts"
	runtimeLanguagePython = "python"
)

func runtimeModuleFromEvent(event Event, dependency string) string {
	return runtimeModuleFromEventForLanguage(event, runtimeLanguageJSTS, dependency)
}

func runtimeModuleFromEventForLanguage(event Event, language string, dependency string) string {
	switch normalizeRuntimeLanguage(language) {
	case runtimeLanguagePython:
		return pythonRuntimeModuleFromEvent(event, dependency)
	default:
		return jsRuntimeModuleFromEvent(event, dependency)
	}
}

func jsRuntimeModuleFromEvent(event Event, dependency string) string {
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
	return runtimeSymbolFromModuleForLanguage(module, runtimeLanguageJSTS, dependency)
}

func runtimeSymbolFromModuleForLanguage(module, language, dependency string) string {
	if normalizeRuntimeLanguage(language) == runtimeLanguagePython {
		return pythonRuntimeSymbolFromModule(module)
	}
	return jsRuntimeSymbolFromModule(module, dependency)
}

func jsRuntimeSymbolFromModule(module, dependency string) string {
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
	return dependencyFromEventForLanguage(event, runtimeLanguageJSTS)
}

func dependencyFromEventForLanguage(event Event, language string) string {
	if dependency := normalizeRuntimeDependency(event.Dependency, language); dependency != "" {
		return dependency
	}
	switch normalizeRuntimeLanguage(language) {
	case runtimeLanguagePython:
		return pythonDependencyFromEvent(event)
	default:
		return jsDependencyFromEvent(event)
	}
}

func jsDependencyFromEvent(event Event) string {
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

func normalizeRuntimeLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "", "js", "ts", "javascript", "typescript", runtimeLanguageJSTS:
		return runtimeLanguageJSTS
	case "py", runtimeLanguagePython:
		return runtimeLanguagePython
	default:
		return strings.ToLower(strings.TrimSpace(language))
	}
}

func normalizeRuntimeDependency(dependency, language string) string {
	dependency = strings.TrimSpace(dependency)
	if dependency == "" {
		return ""
	}
	if normalizeRuntimeLanguage(language) == runtimeLanguagePython {
		return normalizePythonRuntimeDependency(dependency)
	}
	return dependency
}

func pythonDependencyFromEvent(event Event) string {
	if dependency := dependencyFromPythonModule(event.Module); dependency != "" {
		return dependency
	}
	return dependencyFromPythonResolvedPath(event.Resolved)
}

func pythonRuntimeModuleFromEvent(event Event, dependency string) string {
	module := strings.TrimSpace(event.Module)
	if module != "" {
		if dependencyFromPythonModule(module) == dependency || normalizeRuntimeDependency(event.Dependency, runtimeLanguagePython) == dependency {
			return module
		}
	}
	if module := pythonModuleFromResolvedPath(event.Resolved, dependency); module != "" {
		return module
	}
	return dependency
}

func pythonRuntimeSymbolFromModule(module string) string {
	module = trimPythonRuntimeModuleSuffix(module)
	parts := pythonModuleParts(module)
	if len(parts) < 2 {
		return ""
	}
	symbol := parts[len(parts)-1]
	symbol = strings.TrimSuffix(symbol, path.Ext(symbol))
	if symbol == "" || symbol == "__init__" {
		return ""
	}
	return symbol
}

func trimPythonRuntimeModuleSuffix(module string) string {
	module = strings.TrimSpace(module)
	for _, suffix := range []string{".pyc", ".pyo", ".py"} {
		if strings.HasSuffix(module, suffix) {
			return strings.TrimSuffix(module, suffix)
		}
	}
	return module
}

func dependencyFromPythonModule(module string) string {
	parts := pythonModuleParts(module)
	if len(parts) == 0 {
		return ""
	}
	return normalizePythonRuntimeDependency(parts[0])
}

func pythonModuleParts(module string) []string {
	module = strings.TrimSpace(module)
	if module == "" || strings.HasPrefix(module, ".") || strings.HasPrefix(module, "/") || strings.Contains(module, ":") {
		return nil
	}
	return strings.FieldsFunc(module, func(r rune) bool {
		return r == '.' || r == '/' || r == '\\'
	})
}

func dependencyFromPythonResolvedPath(value string) string {
	module := pythonModuleFromResolvedPath(value, "")
	if module == "" {
		return ""
	}
	return dependencyFromPythonModule(module)
}

func pythonModuleFromResolvedPath(value, dependency string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "file://")
	value = filepath.ToSlash(value)
	for _, marker := range []string{"/site-packages/", "/dist-packages/"} {
		pos := strings.LastIndex(value, marker)
		if pos < 0 {
			continue
		}
		rest := strings.TrimPrefix(value[pos+len(marker):], "/")
		if rest == "" {
			return ""
		}
		module := strings.TrimSuffix(strings.Split(rest, "/")[0], ".py")
		module = strings.TrimSuffix(module, ".pyc")
		module = strings.TrimSuffix(module, ".pyo")
		if module == "__pycache__" || strings.HasSuffix(module, ".dist-info") || strings.HasSuffix(module, ".egg-info") {
			return ""
		}
		if dependency != "" && dependencyFromPythonModule(module) != dependency {
			return ""
		}
		return module
	}
	return ""
}

func normalizePythonRuntimeDependency(dependency string) string {
	replacer := strings.NewReplacer("_", "-", ".", "-")
	normalized := replacer.Replace(strings.ToLower(strings.TrimSpace(dependency)))
	if canonical, ok := pythonRuntimeImportAliases[normalized]; ok {
		return canonical
	}
	return normalized
}

var pythonRuntimeImportAliases = map[string]string{
	"bs4":      "beautifulsoup4",
	"cv2":      "opencv-python",
	"dateutil": "python-dateutil",
	"dotenv":   "python-dotenv",
	"pil":      "pillow",
	"sklearn":  "scikit-learn",
}
