package FilterFiles

import (
	"slices"
	"strings"
	"testing"

	c "github.com/dimagog/bufa/internal/contract"
)

func compile(rules ...string) *Filter { return Compile(rules) }

func tryCompile(rules ...string) (*Filter, error) {
	var f *Filter
	err := c.Rescue(func() { f = compile(rules...) })
	return f, err
}

func assertIncluded(t *testing.T, f *Filter, path string, want bool) {
	t.Helper()
	if got := f.Included(path); got != want {
		t.Errorf("Included(%q) = %v, want %v", path, got, want)
	}
}

func TestIncluded_RootAnchoredOnlyMatchesRoot(t *testing.T) {
	f := compile("+**", "-/*.md")
	assertIncluded(t, f, "a.md", false)
	assertIncluded(t, f, "dir/a.md", true)
}

func TestIncluded_UnanchoredBasenameMatchesAnywhere(t *testing.T) {
	f := compile("-**", "+file.ext")
	assertIncluded(t, f, "file.ext", true)
	assertIncluded(t, f, "dir/file.ext", true)
	assertIncluded(t, f, "dir/sub/file.ext", true)
	assertIncluded(t, f, "other.ext", false)
	assertIncluded(t, f, "dir/other.ext", false)
}

func TestIncluded_TrailingDoubleStarRequiresDescendant(t *testing.T) {
	f := compile("-**", "+/*/**")
	assertIncluded(t, f, "a.md", false)
	assertIncluded(t, f, "dir/b.md", true)
	assertIncluded(t, f, "dir/sub/c.md", true)
}

func TestIncluded_LastMatchWins(t *testing.T) {
	f := compile("+**", "-**/*.md", "+dir/keep.md")
	assertIncluded(t, f, "dir/keep.md", true)
	assertIncluded(t, f, "dir/drop.md", false)
	assertIncluded(t, f, "dir/x.png", true)
}

func TestIncluded_CaseInsensitive(t *testing.T) {
	f := compile("+**", "-**/SKIP.MD", "-**/DROP/**")
	assertIncluded(t, f, "dir/skip.md", false)
	assertIncluded(t, f, "drop/item.bin", false)
	assertIncluded(t, f, "dir/keep.md", true)
}

func TestIncluded_TrailingSlashAsDescendants(t *testing.T) {
	f := compile("+**", "-/drop/", "-**/__pycache__/")
	assertIncluded(t, f, "drop/item.bin", false)
	assertIncluded(t, f, "__pycache__/x.pyc", false)
	assertIncluded(t, f, "dir/__pycache__/y.pyc", false)
	assertIncluded(t, f, "dir/drop/item.bin", true)
	assertIncluded(t, f, "drop.md", true)
}

func TestIncluded_StarVsDoubleStarSlash(t *testing.T) {
	single := compile("-**", "+/*")
	assertIncluded(t, single, "a.md", true)
	assertIncluded(t, single, "dir/b.md", false)

	double := compile("-**", "+/**")
	assertIncluded(t, double, "a.md", true)
	assertIncluded(t, double, "dir/b.md", true)
}

func TestIncluded_NoRulesExcludesAll(t *testing.T) {
	f := compile()
	assertIncluded(t, f, "a.md", false)
	assertIncluded(t, f, "dir/b.md", false)
	assertIncluded(t, f, "", false)
}

func TestIncluded_LeadingSlashOptional(t *testing.T) {
	rules := []string{"+**", "-**/SKIP.MD", "-/drop/", "-**/.**", "-/*.tmp"}
	f := compile(rules...)
	cases := []string{
		"a.md",
		"dir/b.md",
		"dir/SKIP.md",
		"drop/x.bin",
		"dir/drop/x.bin",
		".hidden",
		"dir/.secret",
		"x.tmp",
		"dir/x.tmp",
	}
	for _, p := range cases {
		without := f.Included(p)
		with := f.Included("/" + p)
		if without != with {
			t.Errorf("path %q: Included without leading / = %v, with = %v", p, without, with)
		}
	}
}

func TestCompile_ParseErrors(t *testing.T) {
	bad := []string{"", "x*", "+", "+/"}
	for _, raw := range bad {
		_, err := tryCompile(raw)
		if err == nil {
			t.Errorf("Compile(%q) expected error, got nil", raw)
			continue
		}
		if !strings.Contains(err.Error(), "FilterFiles:") {
			t.Errorf("Compile(%q) error = %q, want FilterFiles-prefixed message", raw, err.Error())
		}
	}
}

func TestCompile_ImplicitIncludePrependedWhenFirstIsExclude(t *testing.T) {
	f := compile("-/skip/", "-**/.**")
	assertIncluded(t, f, "a.md", true)
	assertIncluded(t, f, "dir/b.md", true)
	assertIncluded(t, f, "skip/x", false)
	assertIncluded(t, f, ".hidden", false)
}

func TestCompile_NoImplicitWhenFirstIsInclude(t *testing.T) {
	got := compile("+**", "-/skip/")
	want := compile("+**", "-/skip/")

	cases := []string{"a.md", "dir/b.md", "skip/x", "dir/skip/keep"}
	for _, p := range cases {
		if got.Included(p) != want.Included(p) {
			t.Errorf("path %q diverges between explicit and implicit forms", p)
		}
	}
	assertIncluded(t, got, "a.md", true)
	assertIncluded(t, got, "skip/x", false)
	assertIncluded(t, got, "dir/skip/keep", true)
}

func TestDefaultRules_ExcludeDotAndUnderscoreSegments(t *testing.T) {
	f := compile(DefaultRules...)
	assertIncluded(t, f, "a.md", true)
	assertIncluded(t, f, "dir/b.md", true)
	assertIncluded(t, f, ".hidden", false)
	assertIncluded(t, f, "dir/.secret", false)
	assertIncluded(t, f, "__pycache__/x.pyc", false)
	assertIncluded(t, f, "dir/__pycache__/y.pyc", false)
}

func TestDefaultRules_Values(t *testing.T) {
	want := []string{"-**/.**", "-**/_**"}
	if !slices.Equal(DefaultRules, want) {
		t.Errorf("DefaultRules = %q, want %q", DefaultRules, want)
	}
}

func TestCompose_DefaultAppliesWhenUserEmpty(t *testing.T) {
	f := Compose([]string{"-.**"}, nil, nil)
	assertIncluded(t, f, "a.txt", true)
	assertIncluded(t, f, "dir/b.txt", true)
	assertIncluded(t, f, ".secret", false)
	assertIncluded(t, f, "dir/.git/cfg", false)
}

func TestCompose_DefaultAppliesUnderBlacklist(t *testing.T) {
	f := Compose([]string{"-.**"}, []string{"-*.md"}, nil)
	assertIncluded(t, f, "a.txt", true)
	assertIncluded(t, f, "README.md", false) // user rule
	assertIncluded(t, f, ".secret", false)   // default rule
}

func TestCompose_ExcludeDefaultHarmlessUnderWhitelist(t *testing.T) {
	f := Compose([]string{"-.**"}, []string{"+/out.txt"}, nil)
	assertIncluded(t, f, "out.txt", true)
	assertIncluded(t, f, "other.txt", false) // whitelist preserved
	assertIncluded(t, f, ".secret", false)
}

func TestCompose_IncludeDefaultHonoredUnderWhitelist(t *testing.T) {
	f := Compose([]string{"+license"}, []string{"+/out.txt"}, nil)
	assertIncluded(t, f, "out.txt", true)
	assertIncluded(t, f, "license", true)    // include default kept
	assertIncluded(t, f, "other.txt", false) // still excluded
}

func TestCompose_IncludeDefaultKeepsImplicitInclude(t *testing.T) {
	f := Compose([]string{"+license"}, nil, nil)
	assertIncluded(t, f, "anything.txt", true) // "+**" floor, not whitelist-of-one
	assertIncluded(t, f, "license", true)
}

func TestCompose_UserOverridesDefaultAndOverridesWin(t *testing.T) {
	f := Compose([]string{"-.**"}, []string{"-drop.txt", "+.keep"}, []string{"-/forced.txt"})
	assertIncluded(t, f, ".keep", true)       // user re-includes a hidden path
	assertIncluded(t, f, ".other", false)     // default still excludes other hidden
	assertIncluded(t, f, "drop.txt", false)   // user exclude
	assertIncluded(t, f, "forced.txt", false) // forced override beats "+**"
	assertIncluded(t, f, "a.txt", true)
}

func TestCompile_ErrorMentionsOffendingRule(t *testing.T) {
	_, err := tryCompile("+**", "garbage")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "garbage") {
		t.Errorf("err = %q, want it to contain the offending rule", err.Error())
	}
}
