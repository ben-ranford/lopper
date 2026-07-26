//go:build darwin

package safeio

import "path/filepath"

func openParentRootNoFollowAlias(current Root, currentPath string, parts []string) (Root, string, []string, error) {
	aliasParts := permittedRootAliasParts(parts)
	if len(aliasParts) == 0 {
		return current, currentPath, parts, nil
	}

	for _, aliasPart := range aliasParts {
		partPath := filepath.Join(currentPath, aliasPart)
		next, openErr := openParentRootChildNoFollow(current, aliasPart, partPath)
		if openErr != nil {
			return nil, "", nil, closeRootWithError(current, openErr)
		}
		if err := current.Close(); err != nil {
			return nil, "", nil, closeRootWithError(next, err)
		}
		current = next
		currentPath = partPath
	}
	return current, currentPath, parts[1:], nil
}

func permittedRootAliasParts(parts []string) []string {
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "etc", "tmp", "var":
		return []string{"private", parts[0]}
	default:
		return nil
	}
}
