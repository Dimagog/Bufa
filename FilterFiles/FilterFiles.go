// Package FilterFiles applies rsync-style include/exclude rules to forward-slash relative paths.
package FilterFiles

import (
	"slices"
	"strings"

	"github.com/gobwas/glob"
	"golang.org/x/text/cases"

	c "github.com/dimagog/bufa/internal/contract"
)

// A fresh Caser per call — cases.Caser is not safe for concurrent use.
func foldString(s string) string { return cases.Fold().String(s) }

var DefaultRules = []string{"-**/.**", "-**/_**"}

type Filter struct {
	rules []compiledRule
}

type compiledRule struct {
	include bool
	g       *glob.Pattern
}

func prependIncludeAll(rules []string) []string {
	return slices.Concat([]string{"+**"}, rules)
}

func Compile(rules []string) *Filter {
	if len(rules) > 0 && strings.HasPrefix(rules[0], "-") {
		rules = prependIncludeAll(rules)
	}
	return CompileNoPrepend(rules)
}

// Takes rules literally, so a leading exclude is not mistaken for Compile's positional heuristic.
func CompileNoPrepend(rules []string) *Filter {
	compiled := make([]compiledRule, 0, len(rules))
	for i, raw := range rules {
		compiled = append(compiled, parseRule(raw, i))
	}
	return &Filter{rules: compiled}
}

// The implicit-include baseline is decided by the USER tier and placed at the floor beneath
// defaults, so a leading default cannot hijack it.
func Compose(defaults, user, overrides []string) *Filter {
	rules := slices.Concat(defaults, user, overrides)
	if len(user) == 0 || strings.HasPrefix(user[0], "-") {
		rules = prependIncludeAll(rules)
	}
	return CompileNoPrepend(rules)
}

func (f *Filter) Included(path string) bool {
	if len(f.rules) == 0 {
		return false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = foldString(path)
	for _, rule := range slices.Backward(f.rules) {
		if rule.g.Match(path) {
			return rule.include
		}
	}
	return false
}

func parseRule(raw string, index int) compiledRule {
	c.Require(raw != "", "FilterFiles: rule %d '%s': rule must not be empty", index, raw)
	var include bool
	switch raw[0] {
	case '+':
		include = true
	case '-':
		include = false
	default:
		c.Fail("FilterFiles: rule %d '%s': rule must start with '+' or '-'", index, raw)
	}
	pattern := raw[1:]
	c.Require(pattern != "", "FilterFiles: rule %d '%s': pattern must not be empty", index, raw)
	c.Require(strings.Trim(pattern, "/") != "", "FilterFiles: rule %d '%s': pattern must contain at least one non-empty segment", index, raw)

	g := c.With("FilterFiles: parse rule %d %s", index, raw).Check2(glob.Compile(normalizePattern(pattern), '/'))
	return compiledRule{include: include, g: g}
}

func normalizePattern(pattern string) string {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**") {
		pattern = "**/" + pattern
	}
	return foldString(pattern)
}
