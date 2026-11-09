package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrPackageManaged is returned when the running binaries belong to a system package.
var ErrPackageManaged = errors.New("installed by a system package manager")

// Owner identifies the system package that owns the running prot binary.
type Owner struct {
	Manager string // "pacman", "dpkg" or "rpm"
	Package string
}

// UpdateHint tells the user how to update through the owning package manager.
func (o Owner) UpdateHint() string {
	switch o.Manager {
	case "pacman":
		return fmt.Sprintf("Update it with your package manager instead, for example: yay -Syu %s", o.Package)
	case "dpkg":
		return fmt.Sprintf("Update it with your package manager instead, for example: sudo apt install --only-upgrade %s",
			o.Package)
	default:
		return fmt.Sprintf("Update it with your package manager instead, for example: sudo dnf upgrade %s", o.Package)
	}
}

// Message is the one-line explanation printed instead of self-updating.
func (o Owner) Message() string {
	return fmt.Sprintf("prot is installed by %s (package %q), so it cannot update itself.", o.Manager, o.Package)
}

// runCommand runs a command and returns trimmed stdout; replaced in tests.
var runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

var ownerQueries = []struct {
	manager string
	args    func(path string) []string
	parse   func(out string) string
}{
	{"pacman", func(p string) []string { return []string{"-Qqo", p} }, firstLine},
	{"dpkg", func(p string) []string { return []string{"-S", p} }, func(out string) string {
		pkg, _, _ := strings.Cut(firstLine(out), ":")
		return strings.TrimSpace(pkg)
	}},
	{"rpm", func(p string) []string { return []string{"-qf", p} }, firstLine},
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// PackageOwner reports which system package owns the running executable, or nil if it was not installed
// by a package manager (curl installer, go install, a manual copy).
func PackageOwner(ctx context.Context) *Owner {
	execPath, err := os.Executable()
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = real
	}
	return packageOwnerOf(ctx, execPath)
}

func packageOwnerOf(ctx context.Context, path string) *Owner {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	for _, q := range ownerQueries {
		if _, err := exec.LookPath(q.manager); err != nil {
			continue
		}
		out, err := runCommand(ctx, q.manager, q.args(path)...)
		if err != nil {
			continue
		}
		if pkg := q.parse(out); pkg != "" {
			return &Owner{Manager: q.manager, Package: pkg}
		}
	}
	return nil
}
