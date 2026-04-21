#!/bin/sh
set -eu

cat > /dev/null

( sleep "${FAKE_CHILD_DELAY:-2}"; printf 'alive\n' > "${FAKE_CHILD_MARKER:?}" ) &

sleep "${FAKE_SLEEP_SECONDS:-10}"
