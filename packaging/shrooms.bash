# bash completion for shrooms
#
# Install: copy to /usr/share/bash-completion/completions/shrooms
#          (`sudo make install` does this), or source it from ~/.bashrc.
#
# Completes peer names for the commands that take one by asking the running
# daemon, which is the only source that knows them — a mesh roster is not
# something a static list can hold. When the daemon is unreachable, or the user
# cannot read its socket, name completion simply yields nothing rather than
# printing an error into the middle of the command line.

_shrooms_peers() {
    local out
    # --json rather than parsing the table: the table is for people and is
    # allowed to change, and this must not break when it does.
    out=$(shrooms status --json 2>/dev/null) || return 0
    [ -n "$out" ] || return 0
    if command -v python3 >/dev/null 2>&1; then
        printf '%s' "$out" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for p in d.get("peers") or []:
    n = p.get("name")
    if n:
        print(n)
' 2>/dev/null
    else
        # No python: pull "name":"..." out of the peers array well enough for
        # completion. Wrong is harmless here; the shell just offers less.
        printf '%s' "$out" | grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' \
            | sed 's/.*"\([^"]*\)"$/\1/'
    fi
}

_shrooms() {
    local cur prev words cword
    if declare -F _init_completion >/dev/null 2>&1; then
        _init_completion || return
    else
        cur=${COMP_WORDS[COMP_CWORD]}
        prev=${COMP_WORDS[COMP_CWORD-1]}
        words=("${COMP_WORDS[@]}")
        cword=$COMP_CWORD
    fi

    local commands="init join invite prepare set-key daemon status paths hosts key keys credential admin version help"
    local common="--config --state"

    # A path-valued flag completes as a path, whichever command it belongs to.
    case $prev in
        --config|--socket|--state|--admin-dir|--dir)
            _filedir 2>/dev/null || COMPREPLY=($(compgen -f -- "$cur"))
            return
            ;;
    esac

    # The subcommand itself.
    if [ "$cword" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi

    local cmd=${words[1]}
    case $cmd in
        init)
            COMPREPLY=($(compgen -W "--name --relay --advertise --port --admin-dir --no-admin --socket --mesh $common" -- "$cur"))
            ;;
        join)
            COMPREPLY=($(compgen -W "--invite --name --relay --advertise --port --timeout --socket --local -v $common" -- "$cur"))
            ;;
        prepare)
            COMPREPLY=($(compgen -W "--name --relay --advertise --port $common" -- "$cur"))
            ;;
        invite)
            COMPREPLY=($(compgen -W "--name --ttl --life --serial --qr --admin-dir --socket --mesh $common" -- "$cur"))
            ;;
        daemon)
            COMPREPLY=($(compgen -W "--socket -v $common" -- "$cur"))
            ;;
        status)
            COMPREPLY=($(compgen -W "--json --socket" -- "$cur"))
            ;;
        paths)
            # Takes an optional peer name. Offer names first, flags after a dash,
            # so the common case is one Tab.
            if [[ $cur == -* ]]; then
                COMPREPLY=($(compgen -W "--socket" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "$(_shrooms_peers)" -- "$cur"))
            fi
            ;;
        hosts)
            COMPREPLY=($(compgen -W "--write --socket $common" -- "$cur"))
            ;;
        key)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "show rotate" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "--qr --yes $common" -- "$cur"))
            fi
            ;;
        set-key)
            COMPREPLY=($(compgen -W "--socket $common" -- "$cur"))
            ;;
        keys)
            COMPREPLY=($(compgen -W "$common" -- "$cur"))
            ;;
        credential)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "set" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "$common" -- "$cur"))
            fi
            ;;
        admin)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "init issue revoke show" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "--dir --name --device --wg --serial --life --write --no-passphrase --publish --socket $common" -- "$cur"))
            fi
            ;;
        *)
            COMPREPLY=()
            ;;
    esac
}

complete -F _shrooms shrooms
