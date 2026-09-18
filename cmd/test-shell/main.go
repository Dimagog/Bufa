// Command test-shell is a minimal test interpreter standing in for a custom shell: a provider dir
// publishes it beside a BUFA.shell, and tests drive bufa's shell-definition path through it.
//
//	test-shell [-i] [<script>]
//
// Runs the script's commands, then — with -i — reads more from stdin (a "session") until EOF or
// `exit`, printing $FAKE_PROMPT before each line. One command per line, ${VAR} expanded from the
// environment, blank and #-lines skipped:
//
//	echo <text>             print text
//	write <file> <text>     write text to file (cwd-relative)
//	append <file> <text>    append a line to file
//	env <NAME>              print NAME=<value> or "NAME unset"
//	exit [<code>]           exit with code (default 0)
//
// Exits 2 on a bad command or a failed write, with the message on stderr. The process exit code IS
// the contract (bufa reads a shell's status), so main exits with run's code directly.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out, errOut io.Writer) int {
	interactive := false
	script := ""
	for _, arg := range args {
		if arg == "-i" {
			interactive = true
		} else if script == "" {
			script = arg
		} else {
			fmt.Fprintln(errOut, "test-shell: usage: test-shell [-i] [<script>]")
			return 2
		}
	}
	if script != "" {
		data, err := os.ReadFile(script)
		if err != nil {
			fmt.Fprintln(errOut, "test-shell:", err)
			return 2
		}
		if code, exited := runLines(strings.Split(string(data), "\n"), out, errOut); exited {
			return code
		}
	}
	if interactive {
		prompt := os.Getenv("FAKE_PROMPT")
		scanner := bufio.NewScanner(in)
		for {
			fmt.Fprint(out, prompt)
			if !scanner.Scan() {
				break
			}
			if code, exited := runLines([]string{scanner.Text()}, out, errOut); exited {
				return code
			}
		}
	}
	return 0
}

func runLines(lines []string, out, errOut io.Writer) (code int, exited bool) {
	for _, line := range lines {
		line = strings.TrimSpace(os.Expand(line, os.Getenv))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, rest, _ := strings.Cut(line, " ")
		switch verb {
		case "echo":
			fmt.Fprintln(out, rest)
		case "write", "append":
			file, text, _ := strings.Cut(rest, " ")
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if verb == "append" {
				flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
			}
			f, err := os.OpenFile(file, flags, 0o644)
			if err == nil {
				_, err = fmt.Fprintln(f, text)
				f.Close()
			}
			if err != nil {
				fmt.Fprintln(errOut, "test-shell:", err)
				return 2, true
			}
		case "env":
			if value, ok := os.LookupEnv(rest); ok {
				fmt.Fprintf(out, "%s=%s\n", rest, value)
			} else {
				fmt.Fprintf(out, "%s unset\n", rest)
			}
		case "exit":
			code := 0
			if rest != "" {
				n, err := strconv.Atoi(rest)
				if err != nil {
					fmt.Fprintln(errOut, "test-shell: bad exit code:", rest)
					return 2, true
				}
				code = n
			}
			return code, true
		default:
			fmt.Fprintln(errOut, "test-shell: unknown command:", line)
			return 2, true
		}
	}
	return 0, false
}
