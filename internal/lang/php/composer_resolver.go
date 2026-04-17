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
	for namespace := range r.localNamespace {
		if namespace == "" {
			continue
		}
		if module == namespace || strings.HasPrefix(module, namespace+`\`) {
			return true
		}
	}
	return false
}

func (r *composerResolver) resolveWithPSR4(module string) string {
	longest := ""
	selected := ""
	for prefix, dependency := range r.namespaceToDep {
		normalizedPrefix := normalizeNamespace(prefix)
		if normalizedPrefix == "" {
			continue
		}
		if module == normalizedPrefix || strings.HasPrefix(module, normalizedPrefix+`\`) {
			if len(normalizedPrefix) > len(longest) {
				longest = normalizedPrefix
				selected = dependency
			}
		}
	}
	return selected
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
