// Package Bufa carries the version, embedded from version.txt at the repo root (a go:embed path
// cannot leave the package dir, hence this root package).
package Bufa

import (
	_ "embed"
)

//go:embed version.txt
var Version string
