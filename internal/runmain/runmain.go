// Package runmain bridges a CLI run() function into an os.Exit-style entry point: prints
// "name ERROR: <err>" to stderr (a multi-line err starts on its own line) and exits 1.
package runmain

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type RunFunc func(args []string, in io.Reader, out, errOut io.Writer) error

func Run(name string, run RunFunc) {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		msg := err.Error()
		sep := " "
		if strings.Contains(msg, "\n") {
			sep = "\n"
		}
		fmt.Fprintf(os.Stderr, "%s ERROR:%s%s\n", name, sep, msg)
		os.Exit(1)
	}
}
