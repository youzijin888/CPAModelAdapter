#!/bin/sh
# Install a supplied CPA executable; no downloads, sudo, or Python required.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
binary=${1:-"$script_dir/cpa"}
if [ ! -f "$binary" ]; then
    printf '%s\n' '找不到 CPA 可执行文件。用法：sh install.sh /完整路径/cpa'
    exit 1
fi
chmod u+x "$binary"
exec "$binary" _setup
