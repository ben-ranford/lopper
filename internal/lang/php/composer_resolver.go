package php

import "strings"

type composerResolver struct {
	namespaceToDep        map[string]string
	localNamespace        map[string]struct{}
	declared              map[string]struct{}
	allowPHPShortOpenTags bool
}

type dependencyResolution struct {
	dependency string
	resolved   bool
	limitHit   bool
}

func newComposerResolver(data composerData) composerResolver {
	return composerResolver{
		namespaceToDep:        data.NamespaceToDep,
		localNamespace:        data.LocalNamespaces,
		declared:              data.DeclaredDependencies,
		allowPHPShortOpenTags: data.ShortOpenTags,
	}
}

func (r *composerResolver) dependencyFromModule(module string) (string, bool) {
	resolution := r.resolveModule(module)
	return resolution.dependency, resolution.resolved
}

func (r *composerResolver) resolveModule(module string) dependencyResolution {
	module = normalizeNamespace(module)
	if module == "" {
		return dependencyResolution{}
	}
	ancestors, limitHit := namespaceAncestors(module, maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes)
	if limitHit {
		return dependencyResolution{limitHit: true}
	}
	if r.isLocalNamespaceFromAncestors(ancestors) {
		return dependencyResolution{}
	}
	if dep := r.resolveWithPSR4FromAncestors(ancestors); dep != "" {
		return dependencyResolution{dependency: dep, resolved: true}
	}
	if dep := r.resolveByNamespaceHeuristic(module); dep != "" {
		return dependencyResolution{dependency: dep, resolved: true}
	}
	return dependencyResolution{resolved: true}
}

func (r *composerResolver) isLocalNamespace(module string) bool {
	ancestors, limitHit := namespaceAncestors(module, maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes)
	if limitHit {
		return false
	}
	return r.isLocalNamespaceFromAncestors(ancestors)
}

func (r *composerResolver) resolveWithPSR4(module string) string {
	ancestors, limitHit := namespaceAncestors(module, maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes)
	if limitHit {
		return ""
	}
	return r.resolveWithPSR4FromAncestors(ancestors)
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

func (r *composerResolver) isLocalNamespaceFromAncestors(ancestors []string) bool {
	return hasNamespacePrefix(ancestors, r.localNamespace)
}

func (r *composerResolver) resolveWithPSR4FromAncestors(ancestors []string) string {
	for _, candidate := range ancestors {
		if dependency := lookupNamespaceDependency(r.namespaceToDep, candidate); dependency != "" {
			return dependency
		}
	}
	return ""
}

func lookupNamespaceDependency(namespaceToDep map[string]string, candidate string) string {
	if dependency := namespaceToDep[candidate]; dependency != "" {
		return dependency
	}
	return namespaceToDep[candidate+`\`]
}

func hasNamespacePrefix(ancestors []string, namespaces map[string]struct{}) bool {
	for _, candidate := range ancestors {
		if _, ok := namespaces[candidate]; ok {
			return true
		}
		if _, ok := namespaces[candidate+`\`]; ok {
			return true
		}
	}
	return false
}

func namespaceAncestors(module string, segmentLimit int, byteLimit int) ([]string, bool) {
	module = normalizeNamespace(module)
	if module == "" {
		return nil, false
	}
	if segmentLimit <= 0 || byteLimit <= 0 {
		return nil, true
	}
	lookupBytes := len(module)
	if lookupBytes > byteLimit {
		return nil, true
	}
	segments := 1
	separators := make([]int, 0, segmentLimit-1)
	for i := 0; i < len(module); i++ {
		if module[i] != '\\' {
			continue
		}
		segments++
		if segments > segmentLimit {
			return nil, true
		}
		separators = append(separators, i)
	}
	ancestors := make([]string, 0, len(separators)+1)
	ancestors = append(ancestors, module)
	for i := len(separators) - 1; i >= 0; i-- {
		lookupBytes += separators[i]
		if lookupBytes > byteLimit {
			return nil, true
		}
		ancestors = append(ancestors, module[:separators[i]])
	}
	return ancestors, false
}
