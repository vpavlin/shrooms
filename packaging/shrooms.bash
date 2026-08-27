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
    # The daemon first. `mesh list` reads the config, which holds the network
    # keys and is root-only — so for everybody who is not root it printed
    # nothing, silently, and --mesh completed to nothing at all. The socket is
    # group-readable by design and knows every mesh that is running.
    shrooms status --json 2>/dev/null | _shrooms_json_field label
    # Then the config, which also knows the ones that are switched OFF. Adds
    # duplicates for the running ones; the caller sorts them out.
    shrooms mesh list 2>/dev/null | awk 'NR>1 && NF {print $1}'
}

# One field out of the status JSON, from meshes or peers alike.
_shrooms_json_field() {
    local field=$1
    if command -v python3 >/dev/null 2>&1; then
        python3 -c '
import json, sys
field = sys.argv[1]
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for k in ("meshes", "peers"):
    for item in d.get(k) or []:
        v = item.get(field)
        if v:
            print(v)
' "$field" 2>/dev/null
    else
        grep -o "\"$field\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" \
            | sed 's/.*"\([^"]*\)"$/\1/'
    fi
}

# Candidates that may contain spaces — a reader name is "ACS ACR39U ICC Reader
# 00 00" — so they are split on newlines and quoted. Without this the shell
# takes each word as a separate candidate and completes to nonsense.
_shrooms_offer() {
    local IFS=$'\n'
    COMPREPLY=($(compgen -W "$1" -- "$cur"))
    local i
    for i in "${!COMPREPLY[@]}"; do
        case ${COMPREPLY[i]} in
            *\ *) COMPREPLY[i]=$(printf '%q' "${COMPREPLY[i]}") ;;
        esac
    done
}

# Durations, for the flags that take one. Not a guess at what somebody wants —
# the point is the FORMAT, which is Go's and rejects "30d" with an error that
# does not mention what it would have accepted.
_shrooms_durations="1h 12h 24h 72h 168h 720h 2160h"

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
    _shrooms_peer_names | sort -u
}

# Device keys, for --device. Only reachable through the daemon: the roster
# knows them and nothing else on this machine does.
_shrooms_devices() {
    shrooms status --json 2>/dev/null | _shrooms_json_field device | sort -u
}

_shrooms_peer_names() {
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

    local commands="init join invite prepare daemon status mesh config reload bound paths services hosts key keys credential keycard admin version help"
    local common="--config --state"

    # A path-valued flag completes as a path, whichever command it belongs to.
    # Values, before commands. Every flag whose answers are knowable is
    # answered here rather than per command: --mesh means the same thing
    # wherever it appears, and one place is one fewer to forget.
    case $prev in
        --config|--socket|--state|--admin-dir|--dir|--file|--sign-with)
            _filedir 2>/dev/null || COMPREPLY=($(compgen -f -- "$cur"))
            return
            ;;
        --mesh)
            _shrooms_offer "$(_shrooms_mesh_labels | sort -u)"
            return
            ;;
        --reader)
            _shrooms_offer "$(_shrooms_readers)"
            return
            ;;
        --name)
            # Only where a name means a device already in the roster. For
            # `admin issue` and `init` it is a name being GIVEN to one, and
            # offering existing names there suggests the wrong thing.
            case "${words[1]}:${words[2]}" in
                admin:revoke) _shrooms_offer "$(_shrooms_peers)" ;;
                *)            COMPREPLY=() ;;
            esac
            return
            ;;
        --device)
            _shrooms_offer "$(_shrooms_devices)"
            return
            ;;
        --life|--within|--keep-for|--ttl|--timeout)
            COMPREPLY=($(compgen -W "$_shrooms_durations" -- "$cur"))
            return
            ;;
        --mode)
            COMPREPLY=($(compgen -W "Core Edge" -- "$cur"))
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
            # The token is positional now, so there is nothing to complete for it —
            # offer the flags and let the token be pasted.
            COMPREPLY=($(compgen -W "--name --relay --advertise --port --timeout --socket --local --entry-node --mesh -v $common" -- "$cur"))
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
            COMPREPLY=($(compgen -W "--json --ipv4 --socket" -- "$cur"))
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
            COMPREPLY=($(compgen -W "--write --file --suffix --socket $common" -- "$cur"))
            ;;
        key)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "show rotate" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "--qr --yes $common" -- "$cur"))
            fi
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
            case ${words[2]} in
                init)
                    COMPREPLY=($(compgen -W "--dir --mesh --no-passphrase --keycard --reader" -- "$cur")) ;;
                issue)
                    COMPREPLY=($(compgen -W "--dir --state --config --mesh --name --life --serial --device --wg --seal --write --sign-with --external-signer" -- "$cur")) ;;
                renew)
                    COMPREPLY=($(compgen -W "--dir --mesh --socket --within --all --life --dry-run --sign-with --external-signer" -- "$cur")) ;;
                revoke)
                    COMPREPLY=($(compgen -W "--dir --device --name --mesh --socket --serial --keep-for --publish --rotate --sign-with --external-signer" -- "$cur")) ;;
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
