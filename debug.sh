#!/bin/sh
set -e

make caddy-dbg
dlv --listen=:2345 --headless=true --api-version=2 exec ./caddy-dbg -- "$@"