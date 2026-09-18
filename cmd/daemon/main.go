// Command daemon is the dual-mode reference binary for package Daemon.
//
//	daemon          <sockPath> [<idle>]     # client mode
//	daemon --daemon <sockPath> [<idle>]     # daemon mode
//
// Daemon mode writes "<dir(sockPath)>/daemon-<pid>.pid" while serving, so tests can count live
// daemons portably.
package main

import (
	"fmt"
	"io"
	"net/rpc"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/pflag"

	"github.com/dimagog/bufa/Daemon"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/runmain"
)

const defaultIdle = 5 * time.Second

type Calc Util.Nothing

type AddArgs struct{ A, B int }

func (Calc) Add(args AddArgs, reply *int) error {
	*reply = args.A + args.B
	return nil
}

func register(s *rpc.Server, _ func()) {
	c.Check(s.Register(Calc{}))
}

func main() { runmain.Run("daemon", run) }

func run(args []string, _ io.Reader, out, errOut io.Writer) (err error) {
	defer c.Catch(&err)

	fs := pflag.NewFlagSet("daemon", pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "usage:")
		fmt.Fprintln(errOut, "  daemon          <sockPath> [<idle>]    # client mode")
		fmt.Fprintln(errOut, "  daemon --daemon <sockPath> [<idle>]    # daemon mode")
		fs.PrintDefaults()
	}
	daemonMode := fs.Bool("daemon", false, "run in daemon mode (default: client mode)")

	c.Check(fs.Parse(args))
	posArgs := fs.Args()
	c.Require(len(posArgs) >= 1 && len(posArgs) <= 2,
		"expected <sockPath> [<idle>], got %d positional args", len(posArgs))

	sockPath := posArgs[0]
	idle := defaultIdle
	if len(posArgs) == 2 {
		idle = c.Check2(time.ParseDuration(posArgs[1]))
	}

	if *daemonMode {
		daemonMain(sockPath, idle)
	} else {
		var daemonArgs []string
		if len(posArgs) == 2 {
			daemonArgs = []string{posArgs[1]}
		}
		clientMain(out, sockPath, daemonArgs)
	}
	return nil
}

func daemonMain(sockPath string, idle time.Duration) {
	// The pid file lands beside the socket and is written before Serve creates the dir.
	c.Check(os.MkdirAll(filepath.Dir(sockPath), 0o755))
	pidPath := filepath.Join(filepath.Dir(sockPath),
		fmt.Sprintf("daemon-%d.pid", os.Getpid()))
	c.Check(os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644))
	defer os.Remove(pidPath)

	Daemon.Serve(sockPath, idle, register)
}

func clientMain(out io.Writer, sockPath string, daemonArgs []string) {
	conn, _ := Daemon.Connect(sockPath, daemonArgs...)
	c.Require(conn != nil, "spawned daemon did not start listening on %s", sockPath)
	defer conn.Close()

	client := rpc.NewClient(conn)
	defer client.Close()

	var sum int
	c.Check(client.Call("Calc.Add", AddArgs{2, 3}, &sum))
	fmt.Fprintln(out, "sum:", sum)
}
