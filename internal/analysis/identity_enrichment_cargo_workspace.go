package analysis

import (
	pathpkg "path"
	"path/filepath"
	"strings"
)

type cargoWorkspaceMembership uint8

const (
	cargoWorkspaceMembershipUnknown cargoWorkspaceMembership = iota
	cargoWorkspaceMembershipIncluded
	cargoWorkspaceMembershipExcluded
)

func cargoOwningManifest(repoPath string, manifest *cargoManifestModel, manifests map[string]*cargoManifestModel) *cargoManifestModel {
	if manifest.hasWorkspace {
		return manifest
	}
	if manifest.packageWorkspace != "" {
		return cargoExplicitWorkspaceOwner(repoPath, manifest, manifests)
	}
	return cargoAncestorWorkspaceOwner(repoPath, manifest, manifests)
}

func cargoExplicitWorkspaceOwner(repoPath string, manifest *cargoManifestModel, manifests map[string]*cargoManifestModel) *cargoManifestModel {
	workspacePath := filepath.Clean(filepath.Join(filepath.Dir(manifest.path), filepath.FromSlash(manifest.packageWorkspace), cargoManifestFileName))
	if !pathWithin(repoPath, workspacePath) {
		return nil
	}
	workspace := manifests[workspacePath]
	if workspace == nil || !workspace.hasWorkspace {
		return nil
	}
	return workspace
}

func cargoAncestorWorkspaceOwner(repoPath string, manifest *cargoManifestModel, manifests map[string]*cargoManifestModel) *cargoManifestModel {
	manifestDir := filepath.Dir(manifest.path)
	for parent := filepath.Dir(manifestDir); pathWithin(repoPath, parent); parent = filepath.Dir(parent) {
		workspace := manifests[filepath.Join(parent, cargoManifestFileName)]
		if workspace != nil && workspace.hasWorkspace {
			switch cargoWorkspaceMembershipForManifest(workspace, manifest.path) {
			case cargoWorkspaceMembershipIncluded:
				return workspace
			case cargoWorkspaceMembershipExcluded:
				return manifest
			default:
				return nil
			}
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return manifest
}

func cargoWorkspaceIncludesManifest(workspace *cargoManifestModel, manifestPath string) bool {
	return cargoWorkspaceMembershipForManifest(workspace, manifestPath) == cargoWorkspaceMembershipIncluded
}

func cargoWorkspaceMembershipForManifest(workspace *cargoManifestModel, manifestPath string) cargoWorkspaceMembership {
	workspaceRoot := filepath.Dir(workspace.path)
	memberDir := filepath.Dir(manifestPath)
	relative, err := filepath.Rel(workspaceRoot, memberDir)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return cargoWorkspaceMembershipUnknown
	}
	if relative == "." {
		return cargoWorkspaceMembershipIncluded
	}
	relative = filepath.ToSlash(relative)
	for _, pattern := range workspace.workspaceExcludes {
		if cargoWorkspacePatternMatches(pattern, relative) {
			return cargoWorkspaceMembershipExcluded
		}
	}
	for _, pattern := range workspace.workspaceMembers {
		if cargoWorkspacePatternMatches(pattern, relative) {
			return cargoWorkspaceMembershipIncluded
		}
	}
	return cargoWorkspaceMembershipUnknown
}

func cargoWorkspacePatternMatches(pattern, relative string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimSuffix(pattern, "/"+cargoManifestFileName)
	matched, err := pathpkg.Match(pattern, relative)
	return err == nil && matched
}
