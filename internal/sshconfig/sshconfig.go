// Package sshconfig reads the machine names a user has defined in their OpenSSH client configuration
// (~/.ssh/config), so prot can offer them when a tunnel is created.
//
// Listing the names is a plain parse and runs nothing. Resolving one name (what host, user, port and key ssh will
// use for it) is delegated to `ssh -G`, which is the only authority on Include, Match and wildcard blocks.
package sshconfig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxIncludeDepth = 8
	maxHosts        = 1000
	maxFileSize     = 1 << 20
)

// DefaultPath returns the user's ssh client configuration file.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// Hosts returns the concrete names defined by Host lines in the file at path and the files it includes, in the
// order they appear and without duplicates. Patterns (anything with * ? or a leading !) are not machines and are
// skipped. A missing file is not an error: the user simply has no configuration.
//
// Relative Include paths are resolved against the directory of path, which for ~/.ssh/config is what ssh does.
func Hosts(path string) ([]string, error) {
	p := &parser{base: filepath.Dir(path), seen: map[string]bool{}, hosts: []string{}}
	if err := p.file(path, 0); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	return p.hosts, nil
}

// Contains reports whether alias is one of the names Hosts returns.
func Contains(hosts []string, alias string) bool {
	for _, h := range hosts {
		if h == alias {
			return true
		}
	}
	return false
}

type parser struct {
	base  string
	seen  map[string]bool
	hosts []string
}

func (p *parser) file(path string, depth int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	sc := bufio.NewScanner(io.LimitReader(f, maxFileSize))
	for sc.Scan() {
		keyword, args := splitLine(sc.Text())
		switch keyword {
		case "host":
			p.addHosts(args)
		case "include":
			if depth < maxIncludeDepth {
				p.include(args, depth)
			}
		}
	}
	return sc.Err()
}

func (p *parser) addHosts(patterns []string) {
	for _, name := range patterns {
		if name == "" || strings.ContainsAny(name, "*?!") || strings.HasPrefix(name, "-") {
			continue
		}
		if p.seen[name] || len(p.hosts) >= maxHosts {
			continue
		}
		p.seen[name] = true
		p.hosts = append(p.hosts, name)
	}
}

// include follows an Include line. Like ssh, it expands ~ and globs, and silently ignores what does not exist.
func (p *parser) include(patterns []string, depth int) {
	for _, pattern := range patterns {
		pattern = expandHome(pattern)
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(p.base, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			// An unreadable included file must not hide the machines in the rest of the configuration.
			_ = p.file(m, depth+1)
		}
	}
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// splitLine splits one configuration line into a lower-cased keyword and its arguments. ssh accepts
// "Keyword arg", "Keyword=arg" and "Keyword = arg", double quotes around arguments, and # comments.
func splitLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil
	}

	end := strings.IndexAny(line, " \t=")
	if end < 0 {
		return strings.ToLower(line), nil
	}
	keyword := strings.ToLower(line[:end])

	rest := strings.TrimLeft(line[end:], " \t")
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	return keyword, splitArgs(rest)
}

func splitArgs(s string) []string {
	args := []string{}
	var cur strings.Builder
	inQuote, have := false, false

	flush := func() {
		if have {
			args = append(args, cur.String())
		}
		cur.Reset()
		have = false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote, have = !inQuote, true
		case inQuote:
			cur.WriteByte(c)
		case c == ' ' || c == '\t':
			flush()
		case c == '#' && !have:
			flush()
			return args
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	flush()
	return args
}
