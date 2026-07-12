#!/usr/bin/env bash
# Stop hook: run `just check` (lint + tests). On failure, emit JSON that
# blocks the turn and feeds the output back so Claude must fix it.

set -u

output=$(just check 2>&1)
status=$?

if [ "$status" -ne 0 ]; then
    jq -nc \
        --arg r "$output" \
        '{decision: "block", reason: ("just check failed:\n" + $r)}'
fi

exit 0
