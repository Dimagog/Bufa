// Command filter-files applies include/exclude rules to a set of paths.
//
//	filter-files (--rule R... | --rules FILE) (<root> | --stdin)
package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/dimagog/bufa/FilterFiles"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/logging"
	"github.com/dimagog/bufa/internal/runmain"
)

func main() {
	runmain.Run("filter-files", run)
}

// Blank lines and lines starting with ';' or '#' are skipped.
func readRulesFile(path string) []string {
	file := c.Check2(os.Open(path))
	defer file.Close()
	var rules []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		rules = append(rules, line)
	}
	c.Check(scanner.Err())
	return rules
}

func run(args []string, in io.Reader, out, errOut io.Writer) (err error) {
	defer c.Catch(&err)

	fs := pflag.NewFlagSet("filter-files", pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "usage: filter-files (--rule R... | --rules FILE) (<root> | --stdin)")
		fs.PrintDefaults()
	}

	var rules []string
	fs.StringArrayVarP(&rules, "rule", "r", nil, "`filter` rule (+pattern or -pattern); may be repeated, last match wins")
	rulesFile := fs.String("rules", "", "read rules from `file` (one per line; skip blank lines and lines starting with ';' or '#')")
	stdin := fs.Bool("stdin", false, "read paths from stdin instead of walking a directory")
	logLevel := fs.String("log-level", "", "log level: none, info, debug, error (default: $LOG_LEVEL or none)")

	c.Check(fs.Parse(args))
	logging.Configure(*logLevel, errOut)

	if *rulesFile != "" {
		c.Require(len(rules) == 0, "cannot use both --rules and --rule; pick one")
		rules = readRulesFile(*rulesFile)
	}
	if len(rules) == 0 {
		rules = FilterFiles.DefaultRules
	}

	tail := fs.Args()
	if *stdin {
		c.Require(len(tail) == 0, "cannot use --stdin together with a root directory; pick one")
	} else {
		c.Require(len(tail) == 1, "expected exactly one positional argument (root directory)")
	}

	filter := FilterFiles.Compile(rules)

	if *stdin {
		filterStdin(in, out, filter)
	} else {
		walkRoot(tail[0], out, filter)
	}
	return nil
}

func filterStdin(in io.Reader, out io.Writer, f *FilterFiles.Filter) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	w := bufio.NewWriter(out)
	defer w.Flush()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if f.Included(line) {
			fmt.Fprintln(w, line)
		}
	}
	c.Checkf(scanner.Err(), "read stdin")
}

func walkRoot(root string, out io.Writer, f *FilterFiles.Filter) {
	w := bufio.NewWriter(out)
	defer w.Flush()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel := c.Check2(filepath.Rel(root, path))
		rel = filepath.ToSlash(rel)
		if f.Included(rel) {
			fmt.Fprintln(w, rel)
		}
		return nil
	})
	c.Check(err)
}
