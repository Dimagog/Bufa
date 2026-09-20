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
  # hello/powershell has a [windows] cmd only; hello/all aggregates these same units
  if (and (not-eq $unit all) (or $platform:is-windows (not-eq $unit powershell))) {
    build /hello/$unit $@args
  }
}

# rust links with the machine's own linker, which CI (it sets the variable) has and a user may not
var dirs = [java go clj]
if (has-env BUFA_TEST_EXAMPLES_REQUIRED) {
  set dirs = (conj $dirs rust)
}

for dir $dirs {
  build /$dir $@args
}
