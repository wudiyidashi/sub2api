#!/bin/sh
set -eu

# 构建时的 Linux checksum 与同层驱动对应，不使用 Windows 二进制摘要
if [ -z "${ACCOUNT_DRIVERS_CODEX_RS_HTTP_SHA256:-}" ]; then
    ACCOUNT_DRIVERS_CODEX_RS_HTTP_SHA256=$(cat /app/drivers/codex-rs-http.sha256)
    export ACCOUNT_DRIVERS_CODEX_RS_HTTP_SHA256
fi

if [ "$(id -u)" = "0" ]; then
    mkdir -p /app/data
    chown -R sub2api:sub2api /app/data 2>/dev/null || true
    exec gosu sub2api "$0" "$@"
fi

if [ "$#" -eq 0 ]; then
    set -- /app/sub2api
elif [ "${1#-}" != "$1" ]; then
    set -- /app/sub2api "$@"
fi
exec "$@"
