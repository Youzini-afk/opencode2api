#!/bin/sh
set -eu

state_dir=${STATE_DIR:-/var/lib/opencode2api}
config_path=${CONFIG_PATH:-$state_dir/config.json}
port=${PORT:-8080}
listen_address=${LISTEN_ADDRESS:-0.0.0.0:$port}
webui_listen_address=${WEBUI_LISTEN_ADDRESS:-0.0.0.0:$port}

mkdir -p "$state_dir" "$(dirname "$config_path")"
umask 077

if [ -f "$config_path" ]; then
    active_config=$config_path
else
    bootstrap=false
    for name in \
        OPENCODE2API_SERVER_KEYS \
        OPENCODE2API_ZEN_KEYS \
        OPENCODE2API_GO_KEYS \
        OPENCODE2API_WEBUI_USERNAME \
        OPENCODE2API_WEBUI_PASSWORD \
        OPENCODE2API_WEBUI_ENABLED
    do
        if printenv "$name" >/dev/null 2>&1; then
            bootstrap=true
            break
        fi
    done

    if [ "$bootstrap" != true ]; then
        printf '%s\n' "config.json not found. Set the OPENCODE2API_* bootstrap variables or mount an existing config file before starting the service." >&2
        exit 1
    fi
    printf '%s\n' "config.json not found; initializing it from OPENCODE2API_* environment variables."
    active_config=$config_path
fi

exec /usr/local/bin/opencode2api -config "$active_config" -listen "$listen_address" -web-listen "$webui_listen_address"
