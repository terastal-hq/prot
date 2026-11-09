package sshconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrUnknownHost means the name is not defined by any Host line in the configuration.
var ErrUnknownHost = errors.New("no such host in the ssh configuration")

// sentinelHost is a name no Host line defines. Resolving it shows what ssh applies to every machine (default
// identity files, global options), so those can be told apart from what the configuration says about one alias.
const sentinelHost = "prot-defaults.invalid"

// Resolved is what ssh will use when asked to connect to an alias.
type Resolved struct {
	Alias     string `json:"alias"`
	HostName  string `json:"hostname"`
	User      string `json:"user"`
	Port      uint32 `json:"port"`
	ProxyJump string `json:"proxy_jump,omitempty"`
	// IdentityFiles are the keys the configuration names for this alias. ssh's own default keys are left out.
	IdentityFiles []string `json:"identity_files"`
}

// Resolver asks ssh itself what a configured alias means.
type Resolver struct {
	SSHPath    string // the ssh binary
	ConfigPath string // the configuration file; empty means ssh's default
}

// Resolve returns the settings ssh applies to alias. It refuses names that the configuration does not define, so
// arbitrary text never reaches ssh and Match exec directives only run for machines the user listed.
func (r Resolver) Resolve(ctx context.Context, alias string, known []string) (Resolved, error) {
	if !Contains(known, alias) {
		return Resolved{}, ErrUnknownHost
	}

	settings, err := r.dump(ctx, alias)
	if err != nil {
		return Resolved{}, err
	}

	res := Resolved{Alias: alias, IdentityFiles: []string{}}
	res.HostName = settings.first("hostname")
	res.User = settings.first("user")
	res.ProxyJump = settings.first("proxyjump")
	if port, err := strconv.ParseUint(settings.first("port"), 10, 32); err == nil {
		res.Port = uint32(port)
	}

	// If the defaults cannot be determined, show no keys rather than ssh's built-in list as if it were a choice.
	if defaults, err := r.dump(ctx, sentinelHost); err == nil {
		skip := map[string]bool{}
		for _, k := range defaults["identityfile"] {
			skip[k] = true
		}
		for _, k := range settings["identityfile"] {
			if !skip[k] {
				res.IdentityFiles = append(res.IdentityFiles, k)
			}
		}
	}
	return res, nil
}

type settings map[string][]string

func (s settings) first(key string) string {
	if v := s[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// dump runs `ssh -G host` and parses its "key value" lines.
func (r Resolver) dump(ctx context.Context, host string) (settings, error) {
	if r.SSHPath == "" {
		return nil, errors.New("ssh is not installed")
	}
	if strings.HasPrefix(host, "-") {
		return nil, fmt.Errorf("invalid host %q", host)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	args := []string{"-G"}
	if r.ConfigPath != "" {
		args = append(args, "-F", r.ConfigPath)
	}
	args = append(args, "--", host)

	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.SSHPath, args...)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh -G %s: %w: %s", host, err, strings.TrimSpace(stderr.String()))
	}

	result := settings{}
	for _, line := range strings.Split(out.String(), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			result[key] = append(result[key], strings.TrimSpace(value))
		}
	}
	return result, nil
}
