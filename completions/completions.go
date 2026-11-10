// Package completions holds prot's shell completion scripts. They are plain files here so the packages can install
// them as they are, and they are embedded so 'prot completion <shell>' prints exactly the same text.
//
// The scripts contain no knowledge of prot's commands. Each one asks 'prot __complete' for the suggestions, which
// keeps bash, zsh and fish in agreement and the logic in Go, where it is tested.
package completions

import "embed"

//go:embed prot.bash _prot prot.fish
var files embed.FS

// Shells lists the shells a script exists for.
var Shells = []string{"bash", "zsh", "fish"}

// Script returns the completion script for a shell, and false for a shell there is none for.
func Script(shell string) (string, bool) {
	name := map[string]string{"bash": "prot.bash", "zsh": "_prot", "fish": "prot.fish"}[shell]
	if name == "" {
		return "", false
	}
	data, err := files.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(data), true
}
