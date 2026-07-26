package runtime

import "path/filepath"

func runtimeWindowsNodeCLIResolvedPath(executable, candidate, resolvedPath string) (string, *runtimeExecutableSource, bool, error) {
	cliPath, ok := runtimeWindowsNodeCLICanonicalPath(executable, candidate, resolvedPath)
	if !ok {
		return "", nil, false, nil
	}
	source, err := openTrustedRuntimeExecutableSource(cliPath)
	if err != nil {
		return "", nil, false, err
	}
	return cliPath, source, true, nil
}

func runtimeWindowsNodeCLICanonicalPath(executable, candidate, resolvedPath string) (string, bool) {
	if !runtimeWindowsNodeCLILauncherTarget(executable, resolvedPath) {
		return "", false
	}
	installationRoot, ok := runtimeWindowsNodeCLIInstallationRoot(candidate, resolvedPath)
	if !ok {
		return "", false
	}
	cliPath := filepath.Join(installationRoot, "node_modules", "npm", "bin", executable+"-cli.js")
	return cliPath, true
}

func runtimeWindowsNodeCLILauncherTarget(executable, resolvedPath string) bool {
	switch executable {
	case "npm", "npx":
	default:
		return false
	}
	base := filepath.Base(resolvedPath)
	return sameRuntimeExecutablePath(base, executable+".cmd") ||
		sameRuntimeExecutablePath(base, executable+".bat")
}

func runtimeWindowsNodeCLIInstallationRoot(candidate, resolvedPath string) (string, bool) {
	candidateDir, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil || !filepath.IsAbs(candidateDir) {
		return "", false
	}
	resolvedDir := filepath.Dir(resolvedPath)
	if !sameRuntimeExecutablePath(candidateDir, resolvedDir) {
		return "", false
	}
	return canonicalRuntimeInstallationRoot(resolvedDir)
}

func runtimePlatformNodeCLIInstallationRoot(executable, candidate, resolvedPath string) (string, bool) {
	if !isWindowsRuntime() {
		return "", false
	}
	if root, ok := runtimeWindowsCanonicalCLIInstallationRoot(executable, resolvedPath); ok {
		return root, true
	}
	if !runtimeWindowsNodeCLILauncherTarget(executable, resolvedPath) {
		return "", false
	}
	return runtimeWindowsNodeCLIInstallationRoot(candidate, resolvedPath)
}

func runtimePlatformNodeExecutableInstallationRoot(nodePath string) (string, bool) {
	if !isWindowsRuntime() {
		return "", false
	}
	nodePath = filepath.Clean(nodePath)
	if filepath.Base(nodePath) != "node.exe" {
		return "", false
	}
	return canonicalRuntimeInstallationRoot(filepath.Dir(nodePath))
}

func runtimeNodeSearchDirsForCLI(launcher resolvedRuntimeExecutable) []string {
	if !isWindowsRuntime() {
		return []string{
			launcher.selectedLauncherRoot,
			filepath.Join(launcher.canonicalInstallationRoot, "bin"),
		}
	}
	return []string{
		launcher.selectedLauncherRoot,
		launcher.canonicalInstallationRoot,
	}
}

func runtimeWindowsCanonicalCLIInstallationRoot(executable, resolvedPath string) (string, bool) {
	if !runtimeNodeCLIScriptTarget(executable, resolvedPath) {
		return "", false
	}
	binDir := filepath.Clean(filepath.Dir(resolvedPath))
	if filepath.Base(binDir) != "bin" {
		return "", false
	}
	npmDir := filepath.Dir(binDir)
	if filepath.Base(npmDir) != "npm" || filepath.Base(filepath.Dir(npmDir)) != "node_modules" {
		return "", false
	}
	return canonicalRuntimeInstallationRoot(filepath.Dir(filepath.Dir(npmDir)))
}
