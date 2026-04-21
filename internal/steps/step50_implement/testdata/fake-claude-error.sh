#!/bin/sh
set -eu

cat >/dev/null
printf 'rate_limit\n' >&2
exit 1
