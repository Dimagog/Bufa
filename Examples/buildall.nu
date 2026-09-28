def build [...a: string] {
  print ""
  try {
    ^bufa ...$a
  } catch {
    print "!!! FAILED !!!"
    exit 1
  }
}

def --wrapped main [...args: string] {
  let on_windows = $nu.os-info.family == "windows"
  let units = glob hello/*.BUFA | path parse | get stem | sort
  for unit in $units {
    # hello/powershell has a [windows] cmd only; hello/all aggregates these same units
    if $unit != "all" and ($on_windows or $unit != "powershell") {
      build $"/hello/($unit)" ...$args
    }
  }
  # deploy/* are tasks: the token after the dir is their `out` argument
  let drop = $nu.temp-path | path join bufa-deploy
  for unit in (glob deploy/*.BUFA | path parse | get stem | sort) {
    build $"/deploy/($unit)" $drop ...$args
  }
  # rust links with the machine's own linker, which CI (it sets the variable) has and a user may not
  let in_ci = ($env.BUFA_TEST_EXAMPLES_REQUIRED? | default "") != ""
  let dirs = [java go clj] ++ (if $in_ci { [rust] } else { [] })
  for dir in $dirs {
    build $"/($dir)" ...$args
  }
}
