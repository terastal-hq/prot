package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/terastal-hq/prot/completions"
	"github.com/terastal-hq/prot/internal/discovery"
	"github.com/terastal-hq/prot/internal/registry"
)

const completionUsage = `Usage: prot completion <bash|zsh|fish>

Print the completion script for a shell. Tab then completes commands and options, and, from the running daemon,
the aliases of your services and tunnels and the machines in your ~/.ssh/config.

  bash   echo 'source <(prot completion bash)' >> ~/.bashrc
  zsh    echo 'source <(prot completion zsh)' >> ~/.zshrc        (after compinit)
  fish   prot completion fish > ~/.config/fish/completions/prot.fish

The packages and the installer already put the scripts where each shell looks for them.`

// completionTimeout bounds each lookup against the daemon, so Tab never hangs the shell on a wedged daemon.
const completionTimeout = 1500 * time.Millisecond

// directive tells the shell what to offer besides the listed candidates. It is the last line of the output of
// 'prot __complete', as ":<number>".
type directive int

const (
	directiveNone  directive = iota // only the candidates
	directiveFiles                  // and file names
	directiveDirs                   // and directory names
)

// candidate is one suggestion. The description is shown by shells that can (zsh and fish).
type candidate struct {
	Value string
	Desc  string
}

// completionSource supplies the suggestions that depend on what the daemon knows.
type completionSource interface {
	list(kind listKind) []candidate
}

// cmdCompletion prints a completion script for a shell.
func cmdCompletion(args []string) {
	if len(args) == 0 {
		fatalf("%s", completionUsage)
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(completionUsage)
		return
	}
	script, ok := completions.Script(args[0])
	if !ok {
		fatalf("Unknown shell %q\n\n%s", args[0], completionUsage)
	}
	fmt.Print(script)
}

// cmdComplete is what the completion scripts call. It takes the words of the command line after "prot", the last
// being the one under the cursor (empty when the cursor follows a space), and prints one suggestion per line as
// "value", or "value<TAB>description", followed by a ":<directive>" line. It never prints anything else and never
// fails: a completion that errors would spill text onto the user's prompt.
func cmdComplete(args []string) {
	candidates, dir := complete(args, daemonSource{})
	_ = writeCompletion(os.Stdout, candidates, dir)
}

func writeCompletion(w io.Writer, candidates []candidate, dir directive) error {
	var b strings.Builder
	for _, c := range candidates {
		b.WriteString(c.Value)
		if c.Desc != "" {
			b.WriteString("\t" + c.Desc)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, ":%d\n", dir)
	_, err := io.WriteString(w, b.String())
	return err
}

// complete answers one completion request. words are the words after "prot", the last one being the word under the
// cursor.
func complete(words []string, src completionSource) ([]candidate, directive) {
	words = joinSplitEquals(words)
	if len(words) == 0 {
		words = []string{""}
	}
	cur, prior := words[len(words)-1], words[:len(words)-1]

	line := parseLine(prior)
	switch {
	case line.lost:
		return nil, directiveNone
	case line.pending != nil:
		return completeValue(*line.pending, cur, "", src)
	case line.active == nil:
		return completeCommand(commandSpecs, cur), directiveNone
	case len(line.active.Subs) > 0:
		return completeCommand(line.active.Subs, cur), directiveNone
	case strings.HasPrefix(cur, "-"):
		name, partial, hasValue := strings.Cut(cur, "=")
		if !hasValue {
			return completeFlags(line, cur), directiveNone
		}
		flag := line.active.flag(name)
		if flag == nil {
			return nil, directiveNone
		}
		return completeValue(*flag, partial, name+"=", src)
	}

	switch kind := line.active.argKind(line.positional); kind {
	case listName:
		return nil, directiveNone
	case listNone:
		if cur == "" {
			return completeFlags(line, cur), directiveNone
		}
		return nil, directiveNone
	default:
		return withPrefix(listFor(kind, src), "", cur), directiveNone
	}
}

// listFor returns the suggestions of one kind. The shells are known without asking the daemon.
func listFor(kind listKind, src completionSource) []candidate {
	if kind != listShells {
		return src.list(kind)
	}
	found := make([]candidate, 0, len(completions.Shells))
	for _, shell := range completions.Shells {
		found = append(found, candidate{Value: shell})
	}
	return found
}

// commandLine is what the words before the cursor add up to.
type commandLine struct {
	active     *commandSpec // the command, or subcommand, being completed; nil before one is chosen
	positional int          // how many plain arguments follow it already
	pending    *flagSpec    // a flag waiting for its value, which the cursor is on
	used       map[string]bool
	lost       bool // an unknown command: nothing to suggest
}

func parseLine(prior []string) commandLine {
	line := commandLine{used: map[string]bool{}}
	for _, word := range prior {
		if line.pending != nil {
			line.pending = nil // this word was the flag's value
			continue
		}
		if strings.HasPrefix(word, "-") && word != "-" {
			if line.active == nil {
				continue // a top-level option such as --help
			}
			name, _, hasValue := strings.Cut(word, "=")
			if flag := line.active.flag(name); flag != nil {
				line.used[flag.Name] = true
				if flag.Value != valueNone && !hasValue {
					line.pending = flag
				}
			}
			continue
		}

		switch {
		case line.active == nil:
			line.active = findCommand(commandSpecs, word)
		case len(line.active.Subs) > 0:
			line.active = findCommand(line.active.Subs, word)
		default:
			line.positional++
		}
		if line.active == nil {
			line.lost = true
			return line
		}
	}
	return line
}

// joinSplitEquals undoes bash's habit of making "=" a word of its own: "--host", "=", "db" is "--host db", and a
// line ending in "--host=" is "--host" followed by an empty word.
func joinSplitEquals(words []string) []string {
	joined := make([]string, 0, len(words))
	for i, word := range words {
		if word == "=" && i > 0 && strings.HasPrefix(words[i-1], "-") {
			if i == len(words)-1 {
				joined = append(joined, "")
			}
			continue
		}
		joined = append(joined, word)
	}
	return joined
}

func findCommand(specs []commandSpec, word string) *commandSpec {
	for i := range specs {
		if specs[i].Name == word {
			return &specs[i]
		}
		for _, alias := range specs[i].Aliases {
			if alias == word {
				return &specs[i]
			}
		}
	}
	return nil
}

// flag finds the flag a word names, whether it is written with one dash or two.
func (c *commandSpec) flag(word string) *flagSpec {
	word = strings.TrimLeft(word, "-")
	if word == "" {
		return nil
	}
	for i := range c.Flags {
		f := &c.Flags[i]
		if word == strings.TrimLeft(f.Name, "-") || (f.Short != "" && word == strings.TrimLeft(f.Short, "-")) {
			return f
		}
	}
	return nil
}

func (c *commandSpec) argKind(position int) listKind {
	if position < len(c.Args) {
		return c.Args[position]
	}
	return listNone
}

// completeCommand suggests commands. An alias is only suggested when no command matches what was typed, so that
// "prot r" does not list rm next to remove and delete, while "prot rem" still finds remove and "prot -" finds -h.
func completeCommand(specs []commandSpec, cur string) []candidate {
	var found []candidate
	for _, spec := range specs {
		if strings.HasPrefix(spec.Name, cur) {
			found = append(found, candidate{Value: spec.Name, Desc: spec.Help})
		}
	}
	if len(found) > 0 {
		return found
	}
	for _, spec := range specs {
		for _, alias := range spec.Aliases {
			if strings.HasPrefix(alias, cur) {
				found = append(found, candidate{Value: alias, Desc: spec.Help})
			}
		}
	}
	return found
}

// completeFlags suggests the flags of the active command that were not given yet. Short forms are only offered
// once a dash is typed, so an empty Tab does not list every flag twice.
func completeFlags(line commandLine, cur string) []candidate {
	var found []candidate
	for _, flag := range line.active.Flags {
		if line.used[flag.Name] && !flag.Repeat {
			continue
		}
		if strings.HasPrefix(flag.Name, cur) {
			found = append(found, candidate{Value: flag.Name, Desc: flag.Help})
		}
		if cur != "" && flag.Short != "" && strings.HasPrefix(flag.Short, cur) {
			found = append(found, candidate{Value: flag.Short, Desc: flag.Help})
		}
	}
	return found
}

// completeValue suggests what can follow a flag. prefix is what the word already starts with ("--host=" when the
// value is attached), which every suggestion has to repeat for the shell to accept it.
func completeValue(flag flagSpec, partial, prefix string, src completionSource) ([]candidate, directive) {
	switch flag.Value {
	case valueHost:
		return withPrefix(src.list(listHosts), prefix, partial), directiveNone
	case valueFile:
		return nil, fileDirective(directiveFiles, prefix)
	case valueDir:
		return nil, fileDirective(directiveDirs, prefix)
	default:
		return nil, directiveNone
	}
}

// fileDirective drops the file request when the value is attached to its flag ("--identity=~/x"): a shell cannot
// complete a path that starts with the flag's name.
func fileDirective(dir directive, prefix string) directive {
	if prefix != "" {
		return directiveNone
	}
	return dir
}

// withPrefix keeps the candidates that start with partial and puts prefix in front of each.
func withPrefix(all []candidate, prefix, partial string) []candidate {
	var found []candidate
	for _, c := range all {
		if strings.HasPrefix(c.Value, partial) {
			found = append(found, candidate{Value: prefix + c.Value, Desc: c.Desc})
		}
	}
	return found
}

// daemonSource reads the suggestions from the running daemon. Any failure, such as no daemon or a slow one,
// yields no suggestions and no message.
type daemonSource struct{}

func (daemonSource) list(kind listKind) []candidate {
	switch kind {
	case listServices:
		var data struct {
			Services []registry.ServiceView `json:"services"`
		}
		if !fetchForCompletion("/v1/services", &data) {
			return nil
		}
		found := make([]candidate, 0, len(data.Services))
		for _, s := range data.Services {
			name := s.Alias
			if name == "" {
				name = s.ID
			}
			found = append(found, candidate{Value: name, Desc: fmt.Sprintf("%s, %s", s.Name, s.Status)})
		}
		return safeCandidates(found)
	case listTunnels:
		var data struct {
			Tunnels []registry.TunnelView `json:"tunnels"`
		}
		if !fetchForCompletion("/v1/tunnels", &data) {
			return nil
		}
		found := make([]candidate, 0, len(data.Tunnels))
		for _, t := range data.Tunnels {
			found = append(found, candidate{
				Value: t.Config.Alias,
				Desc:  fmt.Sprintf("%s:%d, %s", t.Config.SSHHost, t.Config.RemotePort, t.State),
			})
		}
		return safeCandidates(found)
	case listDiscovered:
		var data struct {
			Services []discovery.ProcessService `json:"services"`
		}
		if !fetchForCompletion("/v1/discovery", &data) {
			return nil
		}
		found := make([]candidate, 0, len(data.Services))
		for _, p := range data.Services {
			found = append(found, candidate{Value: p.ID, Desc: fmt.Sprintf("%s on port %d", p.Name, p.Port)})
		}
		return safeCandidates(found)
	case listHosts:
		var data sshHostList
		if !fetchForCompletion("/v1/ssh/hosts", &data) {
			return nil
		}
		found := make([]candidate, 0, len(data.Hosts))
		for _, host := range data.Hosts {
			found = append(found, candidate{Value: host})
		}
		return safeCandidates(found)
	default:
		return nil
	}
}

// fetchForCompletion reads one daemon endpoint into out within completionTimeout.
func fetchForCompletion(path string, out any) bool {
	client, err := getDaemonClient()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	req, err := client.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return false
	}
	resp, err := callClient.Do(req.WithContext(ctx))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) == nil
}

// safeCandidates drops values a shell could misread (spaces, quotes, glob characters and so on) and flattens
// descriptions to one line. Aliases are restricted to letters, digits and dashes, but machine names come from a
// file the user edits, and a suggestion is inserted into their command line.
func safeCandidates(all []candidate) []candidate {
	safe := make([]candidate, 0, len(all))
	for _, c := range all {
		if c.Value == "" || strings.ContainsAny(c.Value, " \t\r\n\"'`$&|;<>()*?[]{}!#~\\=") {
			continue
		}
		c.Desc = strings.Join(strings.Fields(c.Desc), " ")
		safe = append(safe, c)
	}
	return safe
}
