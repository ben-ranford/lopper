package gitexec

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const shallowDepthFlag = "--depth=1"

func (r *gitRequest) materialize(args []string) bool {
	if r.safe || r.hooks {
		return false
	}
	if len(args) == 2 && args[0] == "init" {
		return !r.file && directoryOperand(args[1])
	}
	values, ok := takePrefix(args, []string{"clone", "--no-tags", shallowDepthFlag, "--"})
	if !ok || len(values) != 2 || !directoryOperand(values[1]) {
		return false
	}
	scheme, valid := repositoryURL(values[0])
	return valid && r.file == (scheme == "file")
}

func (r *gitRequest) dashboard(args []string) bool {
	switch args[0] {
	case "remote":
		return !r.file && validRemote(args[1:])
	case "fetch":
		return validFetch(args[1:])
	case "checkout":
		return len(args) == 4 && args[1] == "--detach" && args[2] == "--force" && checkoutRef(args[3])
	case "reset":
		return len(args) == 3 && args[1] == "--hard" && checkoutRef(args[2])
	case "clean":
		return slices.Equal(args, []string{"clean", "-fdx"})
	default:
		return false
	}
}

func validRemote(args []string) bool {
	if slices.Equal(args, []string{"get-url", "origin"}) {
		return true
	}
	if len(args) != 3 || args[0] != "add" || args[1] != "origin" {
		return false
	}
	_, valid := repositoryURL(args[2])
	return valid
}

func validFetch(args []string) bool {
	if slices.Equal(args, []string{"--prune", shallowDepthFlag, "origin", "HEAD"}) {
		return true
	}
	values, ok := takePrefix(args, []string{"--prune", "--no-tags", shallowDepthFlag, "origin"})
	return ok && len(values) == 1 && fetchRef(values[0])
}

func checkoutRef(value string) bool {
	return value == "HEAD" || value == "FETCH_HEAD"
}

func fetchRef(value string) bool {
	if ValidObjectID(value) {
		return true
	}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(value, prefix) {
			return referenceName(strings.TrimPrefix(value, prefix))
		}
	}
	return false
}

func referenceName(value string) bool {
	if value == "" || value == "@" || strings.HasPrefix(value, "-") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.HasSuffix(value, ".") || strings.ContainsAny(value, "~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func revision(value string) bool {
	name, suffix := value, ""
	index := strings.IndexAny(value, "~^")
	if index >= 0 {
		name, suffix = value[:index], value[index+1:]
	}
	if !referenceName(name) {
		return false
	}
	if index >= 0 {
		for _, char := range suffix {
			if char != '~' && char != '^' && (char < '0' || char > '9') {
				return false
			}
		}
	}
	return true
}

func revisionRange(value string) bool {
	left, right, ok := strings.Cut(value, "..")
	return ok && revision(left) && revision(right)
}

func repositoryURL(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Path == "" || parsed.Path == "/" || strings.ContainsRune(parsed.Path, 0) {
		return "", false
	}
	switch parsed.Scheme {
	case "file":
		valid := parsed.User == nil && (parsed.Host == "" || parsed.Host == "localhost") && filepath.IsAbs(parsed.Path)
		return parsed.Scheme, valid
	case "https", "ssh":
		return parsed.Scheme, networkURL(parsed)
	default:
		return "", false
	}
}

func networkURL(parsed *url.URL) bool {
	if parsed.Hostname() == "" || strings.HasPrefix(parsed.Hostname(), "-") {
		return false
	}
	if parsed.User == nil {
		return true
	}
	_, password := parsed.User.Password()
	user := parsed.User.Username()
	return parsed.Scheme == "ssh" && !password && user != "" && !strings.HasPrefix(user, "-") && !strings.ContainsAny(user, "\x00\r\n")
}
