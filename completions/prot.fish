# fish completion for prot.
#
# Enable it for the current session with:  prot completion fish | source
# Or keep it:  prot completion fish > ~/.config/fish/completions/prot.fish
# The packages and the installer put this file where fish loads it on its own.
#
# This script knows nothing about prot's commands. It asks 'prot __complete' for the suggestions, so fish, bash and
# zsh always agree.

function __prot_complete
    set -l tokens (commandline -opc)
    set -l cur (commandline -ct)

    # The words after "prot", then the one under the cursor. It is quoted so that it still counts when it is empty.
    set -l out (command $tokens[1] __complete $tokens[2..-1] "$cur" 2>/dev/null)

    set -l dir 0
    for line in $out
        if string match -qr '^:[0-9]+$' -- $line
            set dir (string sub -s 2 -- $line)
        else if test -n "$line"
            echo $line
        end
    end

    # 1 asks for file names too, 2 for directory names.
    switch $dir
        case 1
            __fish_complete_path "$cur"
        case 2
            __fish_complete_directories "$cur"
    end
end

complete -c prot -f -a '(__prot_complete)'
