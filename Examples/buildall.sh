#!/usr/bin/env bash

build() {
  echo
  if ! bufa "$@"; then
    echo '!!! FAILED !!!'
    exit 1
  fi
}

for cfg in hello/*.BUFA; do
  unit=$(basename "$cfg" .BUFA)
  # hello/powershell has a [windows] cmd only
  if [ "$unit" != powershell ]; then
    build "/hello/$unit" "$@"
  fi
done

build /java "$@"
