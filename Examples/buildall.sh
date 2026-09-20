#!/usr/bin/env bash

set -eu

build() {
  echo
  if ! bufa "$@"; then
    echo '!!! FAILED !!!'
    exit 1
  fi
}

for cfg in hello/*.BUFA; do
  unit=$(basename "$cfg" .BUFA)
  # hello/powershell has a [windows] cmd only; hello/all aggregates these same units
  if [ "$unit" != powershell ] && [ "$unit" != all ]; then
    build "hello/$unit" "$@"
  fi
done

# rust links with the machine's own linker, which CI (it sets the variable) has and a user may not
dirs="java go clj"
if [ -n "${BUFA_TEST_EXAMPLES_REQUIRED:-}" ]; then
  dirs="$dirs rust"
fi

for dir in $dirs; do
  build "$dir" "$@"
done
