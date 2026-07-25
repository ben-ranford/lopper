//go:build !darwin

package safeio

func openParentRootNoFollowAlias(current Root, currentPath string, parts []string) (Root, string, []string, error) {
	return current, currentPath, parts, nil
}

func permittedRootAliasParts([]string) []string {
	return nil
}
