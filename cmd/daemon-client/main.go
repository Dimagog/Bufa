// Command daemon-client invokes a single Watcher RPC against an already-running daemon.
// It does NOT spawn one — a missing socket is an error.
//
//	daemon-client <sockPath> GetSrcHash <path>
//	daemon-client <sockPath> SetSrcHash <path> <hash>
package main

import (
	"fmt"
	"io"
	"net"
	"net/rpc"

	"github.com/spf13/pflag"

	"github.com/dimagog/bufa/Watcher"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/logging"
	"github.com/dimagog/bufa/internal/runmain"
)

func main() { runmain.Run("daemon-client", run) }

func run(args []string, _ io.Reader, out, errOut io.Writer) (err error) {
	defer c.Catch(&err)

	fs := pflag.NewFlagSet("daemon-client", pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "usage:")
		fmt.Fprintln(errOut, "  daemon-client <sockPath> GetSrcHash <path>")
		fmt.Fprintln(errOut, "  daemon-client <sockPath> SetSrcHash <path> <hash>")
		fs.PrintDefaults()
	}
	logLevel := fs.String("log-level", "", "log level: none, info, debug, error (default: $LOG_LEVEL or none)")

	c.Check(fs.Parse(args))
	logging.Configure(*logLevel, errOut)

	posArgs := fs.Args()
	c.Require(len(posArgs) >= 2, "Expected <sockPath> <op> [<args>...], got %d positional args", len(posArgs))

	sockPath, op := posArgs[0], posArgs[1]

	conn := c.Check2(net.Dial("unix", sockPath))
	defer conn.Close()
	client := rpc.NewClient(conn)
	defer client.Close()

	switch op {
	case "GetSrcHash":
		c.Require(len(posArgs) == 3, "GetSrcHash expects <path>, got %d extra args", len(posArgs)-2)
		var hash string
		c.Check(client.Call("Watcher.GetSrcHash", posArgs[2], &hash))
		fmt.Fprintf(out, "GetSrcHash('%s'): '%s'\n", posArgs[2], hash)
	case "SetSrcHash":
		c.Require(len(posArgs) == 4, "SetSrcHash expects <path> <hash>, got %d extra args", len(posArgs)-2)
		var reply Util.Nothing
		c.Check(client.Call("Watcher.SetSrcHash",
			Watcher.SetSrcHashArgs{Path: posArgs[2], Hash: posArgs[3]}, &reply))
		fmt.Fprintf(out, "SetSrcHash('%s')='%s'\n", posArgs[2], posArgs[3])
	default:
		c.Fail("Unknown op '%s' (want GetSrcHash or SetSrcHash)", op)
	}
	return nil
}
