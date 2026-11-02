package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// pkgNameOK is deliberately strict: catalog data must never smuggle a shell
// metacharacter into a root-level package manager command.
var pkgNameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)

// distroFamily maps /etc/os-release to the dependency key used by the catalog.
func distroFamily() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	var id, like string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = strings.ToLower(v)
		case "ID_LIKE":
			like = strings.ToLower(v)
		}
	}

	tokens := append([]string{id}, strings.Fields(like)...)
	for _, t := range tokens {
		switch t {
		case "debian", "ubuntu", "linuxmint", "pop", "elementary", "zorin", "kali", "raspbian", "neon":
			return "debian"
		case "fedora", "rhel", "centos", "rocky", "almalinux", "ol", "amzn":
			return "fedora"
		case "opensuse", "opensuse-leap", "opensuse-tumbleweed", "suse", "sles":
			return "suse"
		case "arch", "manjaro", "endeavouros", "cachyos", "garuda":
			return "arch"
		}
	}
	return ""
}

func familyLabel(fam string) string {
	switch fam {
	case "debian":
		return "Debian/Ubuntu (apt)"
	case "fedora":
		return "Fedora/RHEL (dnf)"
	case "suse":
		return "openSUSE (zypper)"
	case "arch":
		return "Arch (pacman)"
	}
	return "unknown"
}

// isInstalled asks the local package manager whether pkg is already present.
func isInstalled(fam, pkg string) bool {
	switch fam {
	case "debian":
		out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", pkg).Output()
		return err == nil && strings.Contains(string(out), "install ok installed")
	case "fedora", "suse":
		return exec.Command("rpm", "-q", "--quiet", pkg).Run() == nil
	case "arch":
		return exec.Command("pacman", "-Q", pkg).Run() == nil
	}
	return false
}

func installCommand(fam string, pkgs []string) ([]string, error) {
	var cmd []string
	switch fam {
	case "debian":
		cmd = append([]string{"apt-get", "install", "-y", "--no-install-recommends"}, pkgs...)
	case "fedora":
		cmd = append([]string{"dnf", "install", "-y"}, pkgs...)
	case "suse":
		cmd = append([]string{"zypper", "--non-interactive", "install"}, pkgs...)
	case "arch":
		cmd = append([]string{"pacman", "-S", "--noconfirm"}, pkgs...)
	default:
		return nil, fmt.Errorf("unsupported distribution family %q", fam)
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			return nil, errors.New("installing packages needs root, but sudo was not found")
		}
		cmd = append([]string{"sudo"}, cmd...)
	}
	return cmd, nil
}

// missingDeps lists the catalog packages for this distro that are not installed
// yet. Names are validated here, so nothing unvetted can reach a root command.
func missingDeps(app *App) ([]string, error) {
	fam := distroFamily()
	var missing []string
	for _, p := range app.Deps[fam] {
		if !pkgNameOK.MatchString(p) {
			return nil, fmt.Errorf("catalog contains an invalid package name %q, refusing to run", p)
		}
		if !isInstalled(fam, p) {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

// rootNeeded means the packages are known but root was not approved. The
// command travels in the error so the caller can print something to paste.
type rootNeeded struct{ cmd []string }

func (r *rootNeeded) Error() string {
	return "needs root: " + strings.Join(r.cmd, " ")
}

// installDeps runs the package manager for the packages the catalog declares.
// approved means the human said yes to the install prompt that listed them. A
// blanket GLP_YES is not an approval for root, so it never reaches this point.
func installDeps(missing []string, approved bool) error {
	if len(missing) == 0 {
		return nil
	}
	fam := distroFamily()
	if fam == "" {
		return errors.New("cannot detect your distribution, so required packages cannot be installed automatically")
	}
	cmd, err := installCommand(fam, missing)
	if err != nil {
		return err
	}
	if !approved {
		return &rootNeeded{cmd: cmd}
	}

	fmt.Printf("  installing %s (root)\n", strings.Join(missing, ", "))
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s not installed, run: %s", strings.Join(missing, ", "), strings.Join(cmd, " "))
	}
	return nil
}

// depsSatisfied is the hard check for source builds: a declined root run must
// stop the build instead of letting make produce a confusing error.
func depsSatisfied(app *App) bool {
	fam := distroFamily()
	if fam == "" {
		return true
	}
	for _, p := range app.Deps[fam] {
		if !isInstalled(fam, p) {
			return false
		}
	}
	return true
}
