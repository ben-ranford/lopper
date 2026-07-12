package runtime

import "fmt"

func validatePythonRuntimeProfile(executable string, args []string, options CommandOptions) error {
	switch {
	case isPythonRuntimeExecutable(executable):
		return validatePythonModuleProfile(executable, args, options)
	case executable == "uv":
		return validateUVRuntimeProfile(args, options)
	default:
		return fmt.Errorf("runtime test command %q is not a Python test profile", executable)
	}
}

func validatePythonModuleProfile(executable string, args []string, options CommandOptions) error {
	if len(args) < 2 || args[0] != "-m" {
		return unsupportedPythonModuleProfileError(executable, options)
	}

	switch args[1] {
	case "pytest":
		return nil
	case "unittest":
		if options.PythonRunnerProfiles {
			return nil
		}
		return pythonRunnerProfilesDisabledError(executable + " -m unittest")
	default:
		return unsupportedPythonModuleProfileError(executable, options)
	}
}

func validateUVRuntimeProfile(args []string, options CommandOptions) error {
	if !options.PythonRunnerProfiles {
		return pythonRunnerProfilesDisabledError("uv run")
	}
	if len(args) < 2 || args[0] != "run" {
		return unsupportedUVRuntimeProfileError()
	}

	runnerIndex := 1
	if args[runnerIndex] == "--" {
		runnerIndex++
	}
	if runnerIndex >= len(args) {
		return unsupportedUVRuntimeProfileError()
	}

	switch runner := args[runnerIndex]; runner {
	case "pytest":
		return nil
	case "python", "python3":
		if err := validatePythonModuleProfile(runner, args[runnerIndex+1:], options); err != nil {
			return unsupportedUVRuntimeProfileError()
		}
		return nil
	default:
		return unsupportedUVRuntimeProfileError()
	}
}

func pythonRunnerProfilesDisabledError(profile string) error {
	return fmt.Errorf("runtime test command profile %q requires --enable-feature %s", profile, PythonRunnerProfilesFeature)
}

func unsupportedPythonModuleProfileError(executable string, options CommandOptions) error {
	if options.PythonRunnerProfiles {
		return fmt.Errorf("runtime test command for %q may only run '-m pytest' or '-m unittest'", executable)
	}
	return fmt.Errorf("runtime test command for %q may only run '-m pytest'", executable)
}

func unsupportedUVRuntimeProfileError() error {
	return fmt.Errorf("runtime test command for %q may only use 'uv run [--] pytest' or 'uv run [--] python[3] -m pytest|unittest' without uv wrapper flags", "uv")
}
