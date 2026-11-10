# bash completion for prot.
#
# Enable it for the current session with:  source <(prot completion bash)
# The packages and the installer put this file where bash-completion loads it on its own.
#
# This script knows nothing about prot's commands. It asks 'prot __complete' for the suggestions, so bash, zsh and
# fish always agree. It works with bash 3.2, the version macOS ships.

_prot() {
    local cur="${COMP_WORDS[COMP_CWORD]}" out line dir=0
    COMPREPLY=()

    # bash makes "=" a word of its own, so "--host=db" arrives as three words, and a cursor right after the "=" is
    # on the word "=" itself. The suggestions go after it, which is what bash replaces, so it counts as empty.
    [[ $cur == "=" ]] && cur=""

    out=$("${COMP_WORDS[0]}" __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null) || return 0

    while IFS= read -r line; do
        case $line in
            :[0-9]*) dir=${line#:} ;;
            "") ;;
            *) COMPREPLY+=("${line%%$'\t'*}") ;;
        esac
    done <<<"$out"

    # 1 asks for file names too, 2 for directory names.
    case $dir in
        1 | 2)
            type compopt >/dev/null 2>&1 && compopt -o filenames
            local flag=-f
            [[ $dir == 2 ]] && flag=-d
            while IFS= read -r line; do
                COMPREPLY+=("$line")
            done < <(compgen $flag -- "$cur")
            ;;
    esac
    return 0
}

complete -F _prot prot
