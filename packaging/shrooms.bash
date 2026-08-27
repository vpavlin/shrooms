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

# Mesh labels, including switched-off ones, which need root to read. Silent
# when it cannot: a completion that prints a permission error over the prompt
# is worse than one that offers nothing.
_shrooms_mesh_labels() {
    shrooms mesh list 2>/dev/null | awk 'NR>1 && NF {print $1}'
}

# What `config set` accepts, asked of the binary rather than duplicated here:
# a second list drifts the first time a setting is added, and is wrong exactly
# where somebody looks to find out what exists.
_shrooms_settings() {
    shrooms config settings --names 2>/dev/null
}

# Reader names, for --reader. Free to ask: `keycard readers` costs no pairing
# slot, no PIN attempt and nothing on the card.
_shrooms_readers() {
    shrooms keycard readers 2>/dev/null
}

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

    local commands="init join invite prepare set-key daemon status mesh config reload bound paths services hosts key keys credential keycard admin version help"
    local common="--config --state"

    # A path-valued flag completes as a path, whichever command it belongs to.
    case $prev in
        --config|--socket|--state|--admin-dir|--dir)
            _filedir 2>/dev/null || COMPREPLY=($(compgen -f -- "$cur"))
            return
            ;;
        # Everywhere, rather than per command: --mesh means the same thing in
        # all of them, and listing it once is one fewer place to forget.
        --mesh)
            COMPREPLY=($(compgen -W "$(_shrooms_mesh_labels)" -- "$cur"))
            return
            ;;
        --reader)
            COMPREPLY=($(compgen -W "$(_shrooms_readers)" -- "$cur"))
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
            COMPREPLY=($(compgen -W "--name --relay --advertise --port --admin-dir --no-admin --socket --mesh --keycard --reader $common" -- "$cur"))
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
        mesh|meshes)
            # The labels come from the config rather than from status, because
            # the whole point of this command is the mesh status cannot see.
            # Second word is the verb, third is a label.
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "list enable disable rename remove" -- "$cur"))
            elif [[ $cur == -* ]]; then
                # remove restarts the daemon itself, so it takes a socket.
                case ${words[2]} in
                    remove|rm|leave) COMPREPLY=($(compgen -W "--yes --socket $common" -- "$cur")) ;;
                    rename)          COMPREPLY=($(compgen -W "--admin-dir $common" -- "$cur")) ;;
                    *)               COMPREPLY=($(compgen -W "$common" -- "$cur")) ;;
                esac
            else
                COMPREPLY=($(compgen -W "$(_shrooms_mesh_labels)" -- "$cur"))
            fi
            ;;
        config)
            # The setting names come from the binary rather than a list kept
            # here: a second copy would drift the first time one is added, and
            # be wrong in the place people find out what exists.
            case $cword in
                2) COMPREPLY=($(compgen -W "validate set settings flatten" -- "$cur")) ;;
                3)
                    if [ "${words[2]}" = set ]; then
                        COMPREPLY=($(compgen -W "$(_shrooms_settings)" -- "$cur"))
                    elif [ "${words[2]}" = flatten ]; then
                        COMPREPLY=($(compgen -W "--dry-run --yes --config" -- "$cur"))
                    else
                        COMPREPLY=($(compgen -W "$common" -- "$cur"))
                    fi
                    ;;
                *)
                    if [ "${words[2]}" = set ]; then
                        # A value, and for the on/off ones there is a right
                        # answer worth offering. --mesh completes labels for
                        # the settings that are per mesh.
                        case $prev in
                            --mesh) COMPREPLY=($(compgen -W "$(_shrooms_mesh_labels)" -- "$cur")); return ;;
                        esac
                        case ${words[3]} in
                            relay|portmap|announce-services|announce-bound|mesh)
                                COMPREPLY=($(compgen -W "on off --mesh --socket" -- "$cur")) ;;
                            mode)
                                COMPREPLY=($(compgen -W "Core Edge --socket" -- "$cur")) ;;
                            *)
                                COMPREPLY=($(compgen -W "--mesh --socket" -- "$cur")) ;;
                        esac
                    else
                        COMPREPLY=($(compgen -W "$common" -- "$cur"))
                    fi
                    ;;
            esac
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
                COMPREPLY=($(compgen -W "init issue renew revoke show" -- "$cur"))
                return
            fi
            # --name means a device already in the roster for revoke, and a
            # name being GIVEN to one for issue. Only the first can be
            # completed, and offering the roster for the second would suggest
            # the wrong thing.
            if [ "$prev" = --name ] && [ "${words[2]}" = revoke ]; then
                COMPREPLY=($(compgen -W "$(_shrooms_peers)" -- "$cur"))
                return
            fi
            case ${words[2]} in
                init)
                    COMPREPLY=($(compgen -W "--dir --mesh --no-passphrase --keycard --reader" -- "$cur")) ;;
                issue)
                    COMPREPLY=($(compgen -W "--dir --state --config --mesh --name --life --serial --device --wg --seal --write --sign-with --external-signer" -- "$cur")) ;;
                renew)
                    COMPREPLY=($(compgen -W "--dir --mesh --socket --within --all --life --dry-run --sign-with --external-signer" -- "$cur")) ;;
                revoke)
                    COMPREPLY=($(compgen -W "--dir --device --name --mesh --socket --serial --keep --sign-with --external-signer" -- "$cur")) ;;
                show)
                    COMPREPLY=($(compgen -W "--dir --mesh" -- "$cur")) ;;
                *)
                    COMPREPLY=($(compgen -W "--dir --mesh $common" -- "$cur")) ;;
            esac
            ;;

        keycard)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "status readers pair init free-slots forget reset" -- "$cur"))
                return
            fi
            case ${words[2]} in
                status)     COMPREPLY=($(compgen -W "--reader --json" -- "$cur")) ;;
                readers)    COMPREPLY=() ;;
                pair)       COMPREPLY=($(compgen -W "--reader --dir" -- "$cur")) ;;
                init)       COMPREPLY=($(compgen -W "--reader --restore" -- "$cur")) ;;
                free-slots) COMPREPLY=($(compgen -W "--reader --dir --yes" -- "$cur")) ;;
                forget)     COMPREPLY=($(compgen -W "--dir" -- "$cur")) ;;
                reset)      COMPREPLY=($(compgen -W "--reader" -- "$cur")) ;;
                *)          COMPREPLY=() ;;
            esac
            ;;

        services)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "list add remove" -- "$cur"))
                return
            fi
            if [[ $cur == -* ]]; then
                COMPREPLY=($(compgen -W "--mesh --socket --tls --to --type" -- "$cur"))
            fi
            ;;
        *)
            COMPREPLY=()
            ;;
    esac
}

complete -F _shrooms shrooms
