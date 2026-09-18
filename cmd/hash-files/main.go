// Command hash-files prints the BLAKE3 content hash of a file or directory.
//
//	hash-files <path>
package main

import (
	"fmt"
	"io"

	vfs "github.com/spf13/afero"
	"github.com/spf13/pflag"

	"github.com/dimagog/bufa/Hashing"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/logging"
	"github.com/dimagog/bufa/internal/runmain"
)

func main() { runmain.Run("hash-files", run) }

func run(args []string, _ io.Reader, out, errOut io.Writer) (err error) {
	defer c.Catch(&err)

	fs := pflag.NewFlagSet("hash-files", pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "usage: hash-files <path>")
		fs.PrintDefaults()
	}
	logLevel := fs.String("log-level", "", "log level: none, info, debug, error (default: $LOG_LEVEL or none)")

	c.Check(fs.Parse(args))
	logging.Configure(*logLevel, errOut)

	args = fs.Args()
	c.Require(len(args) == 1, "expected exactly one positional argument (path)")

	fmt.Fprintln(out, Hashing.Hash(vfs.NewOsFs(), args[0]))
	return nil
}
