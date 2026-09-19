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
    # hello/powershell has a [windows] cmd only
    if $on_windows or $unit != "powershell" {
      build $"/hello/($unit)" ...$args
    }
  }
  build /java ...$args
}
