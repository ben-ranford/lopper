package php

import "strings"

type composerResolver struct {
	namespaceToDep map[string]string
	localNamespace map[string]struct{}
	declared       map[string]struct{}
}

func newComposerResolver(data composerData) composerResolver {
	return composerResolver{
		namespaceToDep: data.NamespaceToDep,
		localNamespace: data.LocalNamespaces,
		declared:       data.DeclaredDependencies,
	}
}

func (r *composerResolver) dependencyFromModule(module string) (string, bool) {
	module = normalizeNamespace(module)
	if module == "" {
		return "", false
	}
	if r.isLocalNamespace(module) {
		return "", false
	}
	if dep := r.resolveWithPSR4(module); dep != "" {
		return dep, true
	}
	if dep := r.resolveByNamespaceHeuristic(module); dep != "" {
		return dep, true
	}
	return "", true
}

func (r *composerResolver) isLocalNamespace(module string) bool {
	return containsNamespacePrefix(r.localNamespace, module)
}

func (r *composerResolver) resolveWithPSR4(module string) string {
	for candidate := normalizeNamespace(module); candidate != ""; candidate = namespaceParent(candidate) {
		if dependency := r.namespaceToDep[candidate]; dependency != "" {
			return dependency
		}
		if dependency := r.namespaceToDep[candidate+`\`]; dependency != "" {
			return dependency
		}
	}
	return ""
}

func containsNamespacePrefix(namespaces map[string]struct{}, module string) bool {
	for candidate := normalizeNamespace(module); candidate != ""; candidate = namespaceParent(candidate) {
		if _, ok := namespaces[candidate]; ok {
			return true
		}
		if _, ok := namespaces[candidate+`\`]; ok {
			return true
		}
	}
	return false
}

func namespaceParent(namespace string) string {
	separator := strings.LastIndex(namespace, `\`)
	if separator < 0 {
		return ""
	}
	return namespace[:separator]
}

func (r *composerResolver) resolveByNamespaceHeuristic(module string) string {
	parts := strings.Split(module, `\`)
	if len(parts) < 2 {
		return ""
	}
	vendor := strings.ToLower(strings.TrimSpace(parts[0]))
	name := normalizePackagePart(parts[1])
	if vendor == "" || name == "" {
		return ""
	}
	candidate := normalizeDependencyID(vendor + "/" + name)
	if _, ok := r.declared[candidate]; ok {
		return candidate
	}
	return ""
}

func normalizeNamespace(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, `\`)
	value = strings.TrimSuffix(value, `\`)
	return value
}
