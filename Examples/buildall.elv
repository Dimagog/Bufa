use path
use platform
use str

fn build {|@a|
  echo
  try {
    bufa $@a
  } catch {
    echo '!!! FAILED !!!'
    exit 1
  }
}

for cfg [hello/*.BUFA] {
  var unit = (str:trim-suffix (path:base $cfg) .BUFA)
  # hello/powershell has a [windows] cmd only
  if (or $platform:is-windows (not-eq $unit powershell)) {
    build /hello/$unit $@args
  }
}

build /java $@args
