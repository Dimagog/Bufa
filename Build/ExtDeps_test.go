package Build

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dimagog/bufa/ArtifactCache"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Runtime"
	c "github.com/dimagog/bufa/internal/contract"
)

func pinOf(data []byte) string { return Hashing.FilePrefix + Hashing.HashBytes(data) }

// Shape-valid pins for deps a test never fetches: normalizeExtDeps rejects a malformed F key up front.
var (
	pinA      = pinOf([]byte("a"))
	pinB      = pinOf([]byte("b"))
	pinUnused = pinOf([]byte("unused"))
)

func serveArtifact(t *testing.T, payload *[]byte) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		c.Check2(w.Write(*payload))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func extFixture(t *testing.T, payload *[]byte) (*fixture, *ArtifactCache.Cache, string, *int) {
	t.Helper()
	f := newFixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("BUFA_GLOBAL_CACHE_DIR", cacheDir)
	srv, hits := serveArtifact(t, payload)
	return f, ArtifactCache.New(ArtifactCache.GetCacheDir()), srv.URL + "/art.bin", hits
}

func extToml(url, pin string, extra ...string) string {
	s := "[[deps.ext]]\nurl = '" + url + "'\nhash = '" + pin + "'\n"
	for _, line := range extra {
		s += line + "\n"
	}
	return s
}

func TestBuild_ExtDepProvider_ExportsArtifact(t *testing.T) {
	data := []byte("JARBYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("P", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data), "export = true"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("P") // cmd = false: an exported-[[deps.ext]]-only dir is a complete provider

	published := filepath.Join(f.bld, "out", h, "art.bin")
	info := c.Check2(os.Lstat(published))
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("a default (non-large) artifact must export as real bytes, not a link")
	}
	if info.Mode()&0o200 == 0 {
		t.Error("a copy-staged artifact must be writable (cache read-only must not propagate)")
	}
	if got := c.Check2(os.ReadFile(published)); !bytes.Equal(got, data) {
		t.Errorf("published bytes = %q, want %q", got, data)
	}
	if *hits != 1 {
		t.Fatalf("server hits = %d, want 1", *hits)
	}

	if h2 := f.builder().Build("P"); h2 != h {
		t.Errorf("repeat build hash %q != %q", h2, h)
	}
	if *hits != 1 {
		t.Errorf("repeat build hit the network: %d hits", *hits)
	}

	f.write(t, filepath.Join("P", "BUFA"), "cmd = false\n"+extToml(url+"?mirror=1", pinOf(data), "export = true"))
	if h3 := f.builder().Build("P"); h3 != h {
		t.Errorf("mirror-moved build hash %q != %q (identical content)", h3, h)
	}
	if *hits != 2 {
		t.Errorf("server hits = %d, want 2 (an edited url re-verifies once — the url-link gate)", *hits)
	}
}

func TestBuild_ExtDep_LargeExportsLink(t *testing.T) {
	data := []byte("BIGBYTES")
	f, cache, url, _ := extFixture(t, &data)
	pin := pinOf(data)
	f.write(t, filepath.Join("PLarge", "BUFA"), "cmd = false\n"+extToml(url, pin, "large = true", "export = true"))
	f.write(t, filepath.Join("PPlain", "BUFA"), "cmd = false\n"+extToml(url, pin, "export = true"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	hLarge := b.Build("PLarge")
	hPlain := b.Build("PPlain")

	if hLarge != hPlain {
		t.Errorf("large D key %q != byte-shipped twin %q", hLarge, hPlain)
	}
	published := filepath.Join(f.bld, "out", hLarge, "art.bin")
	if target := c.Check2(os.Readlink(published)); target != cache.EntryPath(pin) {
		t.Errorf("published link targets %q, want cache entry %q", target, cache.EntryPath(pin))
	}
}

func TestBuild_ExtDep_ExportViaDepsExportList(t *testing.T) {
	data := []byte("JARBYTES")
	f, cache, url, _ := extFixture(t, &data)
	pin := pinOf(data)
	ext := extToml(url, pin, "name = 'libs/art.bin'")
	extFlag := extToml(url, pin, "name = 'libs/art.bin'", "export = true")
	f.write(t, filepath.Join("PFlag", "BUFA"), "cmd = false\n"+extFlag)
	f.write(t, filepath.Join("PList", "BUFA"), "cmd = false\n[deps]\nexport = ['libs/art.bin']\n"+ext)
	f.write(t, filepath.Join("PDotList", "BUFA"), "cmd = false\n[deps]\nexport = ['./libs/art.bin']\n"+ext)
	f.write(t, filepath.Join("PBoth", "BUFA"), "cmd = false\n[deps]\nexport = ['libs/art.bin']\n"+extFlag)
	f.write(t, filepath.Join("PLarge", "BUFA"),
		"cmd = false\n[deps]\nexport = ['libs/art.bin']\n"+extToml(url, pin, "name = 'libs/art.bin'", "large = true"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	// Large first: every twin shares one D key, so only the first publish's tree lands in out/.
	hLarge := b.Build("PLarge")
	published := filepath.Join(f.bld, "out", hLarge, "libs", "art.bin")
	if target := c.Check2(os.Readlink(published)); target != cache.EntryPath(pin) {
		t.Errorf("published link targets %q, want cache entry %q", target, cache.EntryPath(pin))
	}
	if got := c.Check2(os.ReadFile(published)); !bytes.Equal(got, data) {
		t.Errorf("published bytes = %q, want %q", got, data)
	}
	for _, dir := range []string{"PFlag", "PList", "PDotList", "PBoth"} {
		if h := b.Build(dir); h != hLarge {
			t.Errorf("%s hash %q != large-exported twin %q", dir, h, hLarge)
		}
	}
}

func TestBuild_ExtDep_ExportListRejectsUnknownName(t *testing.T) {
	f, _, url, _ := extFixture(t, new([]byte))
	f.write(t, filepath.Join("U", "BUFA"),
		"cmd = false\n[deps]\nexport = ['art.bin']\n"+extToml(url, pinUnused, "name = 'libs/art.bin'"))

	b := f.builder()
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }),
		"deps.export 'art.bin' in 'U' is not a deps.bld, deps.src, or deps.ext dependency")
}

func TestBuild_ExtDep_DefaultUnexported_Pruned(t *testing.T) {
	data := []byte("TOOLBYTES")
	f, _, url, _ := extFixture(t, &data)
	winUse := "@echo off\r\ncopy libs\\art.bin out.bin >nul\r\n"
	unixUse := "cp libs/art.bin out.bin\n"
	f.write(t, filepath.Join("U", "BUFA"),
		bufaToml(winUse, unixUse)+extToml(url, pinOf(data), "name = 'libs/art.bin'"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("U")

	if got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, "out.bin"))); !bytes.Equal(got, data) {
		t.Errorf("script output = %q, want artifact bytes %q", got, data)
	}
	if f.exists("out/" + h + "/libs/art.bin") {
		t.Error("a non-exported artifact must be pruned from the output")
	}
	if f.exists("out/" + h + "/libs") {
		t.Error("the dir emptied by the prune must be removed too")
	}
}

func TestBuild_ExtDep_UnexportedParentRemovedByScript(t *testing.T) {
	data := []byte("TOOLBYTES")
	f, _, url, _ := extFixture(t, &data)
	// The script deletes the artifact's parent wholesale: the emptied-parent sweep must stop at the
	// missing dir, not panic.
	winUse := "@echo off\r\ncopy libs\\art.bin out.bin >nul\r\nrmdir /s /q libs\r\n"
	unixUse := "cp libs/art.bin out.bin\nrm -rf libs\n"
	f.write(t, filepath.Join("U", "BUFA"),
		bufaToml(winUse, unixUse)+extToml(url, pinOf(data), "name = 'libs/art.bin'"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("U")

	if got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, "out.bin"))); !bytes.Equal(got, data) {
		t.Errorf("script output = %q, want artifact bytes %q", got, data)
	}
	if f.exists("out/" + h + "/libs") {
		t.Error("script-removed parent must stay gone from the output")
	}
}

func TestBuild_ExtDep_LargeUnexported_ReadThroughAndPruned(t *testing.T) {
	data := []byte("BIGTOOL")
	f, cache, url, _ := extFixture(t, &data)
	winUse := "@echo off\r\ncopy art.bin out.bin >nul\r\n"
	unixUse := "cp art.bin out.bin\n"
	f.write(t, filepath.Join("U", "BUFA"),
		bufaToml(winUse, unixUse)+extToml(url, pinOf(data), "large = true"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("U")

	if got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, "out.bin"))); !bytes.Equal(got, data) {
		t.Errorf("script output = %q, want artifact bytes read through the link %q", got, data)
	}
	if f.exists("out/" + h + "/art.bin") {
		t.Error("a non-exported large artifact must be pruned from the output")
	}
	pin := pinOf(data)
	if !cache.Contains(pin) {
		t.Error("pruning the staged link must not touch the cache entry")
	}
	if got := c.Check2(os.ReadFile(cache.EntryPath(pin))); !bytes.Equal(got, data) {
		t.Errorf("cache bytes after prune = %q, want %q", got, data)
	}
}

func TestBuild_ExtDep_ConsumerStagesProvider(t *testing.T) {
	data := []byte("TOOLBYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("P", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data), "export = true"))
	winUse := "@echo off\r\ncopy ..\\P\\art.bin out.bin >nul\r\n"
	unixUse := "cp ../P/art.bin out.bin\n"
	f.write(t, filepath.Join("C", "BUFA"), "deps.bld = ['/P']\n"+bufaToml(winUse, unixUse))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("C")

	if got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, "out.bin"))); !bytes.Equal(got, data) {
		t.Errorf("consumer output = %q, want artifact bytes %q", got, data)
	}
	if *hits != 1 {
		t.Errorf("server hits = %d, want 1 (one fetch serves provider and consumer)", *hits)
	}
}

func TestBuild_ExtDep_SourceCollisionFails(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "OWN")
	f.write(t, filepath.Join("U", "art.bin"), "squatter")
	f.write(t, filepath.Join("U", "BUFA"),
		bufaToml(winScript, unixScript)+extToml("http://unused.invalid/art.bin", pinUnused))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil {
		t.Fatal("a source occupant at the declared name must fail the build")
	}
	if msg := err.Error(); !strings.Contains(msg, "occupied") || !strings.Contains(msg, "dirty-build") {
		t.Errorf("collision error must name the occupation with the dirty-residue hint, got %q", msg)
	}
}

func TestBuild_ExtDep_SourceCollisionsBeforeFilteringFail(t *testing.T) {
	cases := []struct {
		name   string
		occupy func(*testing.T, *fixture)
	}{
		{"filtered file", func(t *testing.T, f *fixture) {
			f.write(t, filepath.Join("U", "art.bin"), "squatter")
		}},
		{"empty directory", func(_ *testing.T, f *fixture) {
			c.Check(os.MkdirAll(filepath.Join(f.src, "U", "art.bin"), 0o755))
		}},
		{"dangling symlink", func(_ *testing.T, f *fixture) {
			c.Check(os.Symlink(filepath.Join(f.src, "missing-art.bin"), filepath.Join(f.src, "U", "art.bin")))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("ARTBYTES")
			f, _, url, hits := extFixture(t, &data)
			f.write(t, filepath.Join("U", "BUFA"),
				"cmd = false\n[filters]\nsrc = ['-/art.bin']\n"+extToml(url, pinOf(data)))
			b := f.builder()
			skipIfNoSymlinks(t, b.store)
			tc.occupy(t, f)

			err := c.Rescue(func() { b.Build("U") })
			if err == nil {
				t.Fatal("any real-source occupant at the declared name must fail before filtering")
			}
			if msg := err.Error(); !strings.Contains(msg, "occupied") || !strings.Contains(msg, "dirty-build") {
				t.Errorf("collision error must name the occupation with the dirty-residue hint, got %q", msg)
			}
			if *hits != 0 {
				t.Errorf("source collision must fail before fetching: %d server hits", *hits)
			}
		})
	}
}

func TestBuild_ExtDep_NestedDepConflictFails(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U/sub", "SUB")
	f.write(t, filepath.Join("U", "BUFA"),
		"deps.bld = ['sub']\n"+bufaToml(winScript, unixScript)+
			extToml("http://unused.invalid/x.bin", pinUnused, "name = 'sub/x.bin'"))
	f.write(t, filepath.Join("U", "own.txt"), "OWN")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil {
		t.Fatal("an ext name inside a nested dep's staged tree must fail the build")
	}
	if msg := err.Error(); !strings.Contains(msg, "collides") || !strings.Contains(msg, "sub") {
		t.Errorf("conflict error must name the ext entry and the nested dep, got %q", msg)
	}
}

func TestBuild_ExtDep_LinkStagedNestedDepConflictFails(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "sub", "BUFA"), "largeOutput = true\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "sub", "own.txt"), "SUB")
	f.write(t, filepath.Join("U", "BUFA"),
		"deps.bld = ['sub']\n"+bufaToml(winScript, unixScript)+
			extToml("http://unused.invalid/x.bin", pinUnused, "name = 'sub/x.bin'"))
	f.write(t, filepath.Join("U", "own.txt"), "OWN")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Errorf("an ext name below a link-staged nested dep must be a staging-plan conflict, got %v", err)
	}
	Hashing.SafeHashing = true
	defer func() { Hashing.SafeHashing = false }()
	if st := b.store.Check(io.Discard, false); st.HasProblems() {
		t.Errorf("store corrupted by the failed staging attempt: %+v", st)
	}
}

func TestBuild_ExtDep_Validation(t *testing.T) {
	cases := []struct{ name, toml, want string }{
		{"duplicate name", extToml("http://x.invalid/a.jar", pinA) + extToml("http://y.invalid/a.jar", pinB), "duplicate"},
		{"escaping name", extToml("http://x.invalid/a.jar", pinA, "name = '../a.jar'"), "strictly-local"},
		{"reserved BUFA", extToml("http://x.invalid/a.jar", pinA, "name = 'BUFA'"), "reserved"},
		{"reserved case-folded BUFA", extToml("http://x.invalid/a.jar", pinA, "name = 'bUfA'"), "reserved"},
		{"reserved suffix", extToml("http://x.invalid/a.jar", pinA, "name = 'x.BUFA'"), "reserved"},
		{"malformed hash", extToml("http://x.invalid/a.jar", "abc"), "hash must be"},
		{"F-prefixed non-key", extToml("http://x.invalid/a.jar", "Fabc"), "hash must be"},
		{"missing hash", "[[deps.ext]]\nurl = 'http://x.invalid/a.jar'\n", "hash must be"},
		{"cross-platform duplicate", extToml("http://x.invalid/a.jar", pinA) +
			"[[windows.deps.ext]]\nurl = 'http://y.invalid/mirror/a.jar'\nhash = '" + pinB + "'\n" +
			"[[unix.deps.ext]]\nurl = 'http://y.invalid/mirror/a.jar'\nhash = '" + pinB + "'\n", "duplicate"},
		{"case-folded duplicate", extToml("http://x.invalid/a.jar", pinA) +
			extToml("http://y.invalid/b.jar", pinB, "name = 'A.JAR'"), "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write(t, filepath.Join("P", "BUFA"), tc.toml)
			requireErrorContains(t, c.Rescue(func() { f.builder().getBuildConfig("P") }), tc.want)
		})
	}
}

func TestBuild_ExtDep_PlatformMerge(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		extToml("http://x.invalid/shared.jar", pinA)+
			"[[windows.deps.ext]]\nurl = 'http://x.invalid/tool.exe'\nhash = '"+pinB+"'\n"+
			"[[unix.deps.ext]]\nurl = 'http://x.invalid/tool'\nhash = '"+pinB+"'\n")

	cfg := f.builder().getBuildConfig("P")

	if len(cfg.Deps.Ext) != 2 {
		t.Fatalf("effective set has %d entries, want deps.ext ∪ platform = 2", len(cfg.Deps.Ext))
	}
	platformName := "tool.exe"
	if runtime.GOOS != "windows" {
		platformName = "tool"
	}
	if cfg.Deps.Ext[0].Name != "shared.jar" || cfg.Deps.Ext[1].Name != platformName {
		t.Errorf("effective names = %q, %q; want shared.jar, %s", cfg.Deps.Ext[0].Name, cfg.Deps.Ext[1].Name, platformName)
	}
}

func TestBuild_ExtDep_QueryHashSentinelFails(t *testing.T) {
	data := []byte("PINME")
	f, cache, url, _ := extFixture(t, &data)
	f.write(t, filepath.Join("P", "BUFA"), "cmd = false\n"+extToml(url, "?"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("P") })
	if err == nil || !strings.Contains(err.Error(), pinOf(data)) {
		t.Errorf("bootstrap must fail printing the computed hash, got %v", err)
	}
	if !cache.Contains(pinOf(data)) {
		t.Error("bootstrap download must land in the cache under its computed hash")
	}
}

func TestDirtyBuild_ExtDep_PlacesLinkAndSkips(t *testing.T) {
	data := []byte("DIRTYBYTES")
	f, cache, url, hits := extFixture(t, &data)
	pin := pinOf(data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pin, "large = true"))
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)

	db.Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	if target := c.Check2(os.Readlink(placed)); target != cache.EntryPath(pin) {
		t.Errorf("placed link targets %q, want cache entry %q", target, cache.EntryPath(pin))
	}
	if got := c.Check2(os.ReadFile(placed)); !bytes.Equal(got, data) {
		t.Errorf("artifact through link = %q, want %q", got, data)
	}

	f.dirtyBuilder().Build("D")
	if *hits != 1 {
		t.Errorf("repeat dirty build hit the network: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_WithScriptSkipsRepeat(t *testing.T) {
	data := []byte("STABLEBYTES")
	f, _, url, hits := extFixture(t, &data)
	// Regression: a divergence between the pre-build skip check and the post-build recompute would
	// rebuild forever, invisible to the scriptless dirty ext tests.
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winScript, unixScript)+extToml(url, pinOf(data)))
	f.write(t, filepath.Join("D", "own.txt"), "OWN")
	db := f.dirtyBuilder()

	db.Build("D")
	f.dirtyBuilder().Build("D") // fresh builder: only the skip hash can skip

	if runs := f.countRuns(t); runs != 1 {
		t.Errorf("script ran %d times, want 1 (repeat must skip via the skip hash)", runs)
	}
	if *hits != 1 {
		t.Errorf("repeat dirty build hit the network: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_StalePinRepointed(t *testing.T) {
	data := []byte("V1BYTES")
	f, cache, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data), "large = true"))
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	db.Build("D")

	dataV2 := []byte("V2BYTES")
	data = dataV2 // switch the served payload
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(dataV2), "large = true"))

	f.dirtyBuilder().Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	if target := c.Check2(os.Readlink(placed)); target != cache.EntryPath(pinOf(dataV2)) {
		t.Errorf("stale-pin link must be re-pointed, targets %q", target)
	}
	if *hits != 2 {
		t.Errorf("server hits = %d, want 2 (one per pin)", *hits)
	}
}

func TestDirtyBuild_ExtDep_ForeignOccupantFails(t *testing.T) {
	data := []byte("XBYTES")
	f, _, url, _ := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data)))
	f.write(t, filepath.Join("D", "art.bin"), "squatter")
	db := f.dirtyBuilder()

	err := c.Rescue(func() { db.Build("D") })
	if err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Errorf("a foreign occupant at the declared name must fail, got %v", err)
	}
}

func TestDirtyBuild_ExtDep_ForeignSymlinkFails(t *testing.T) {
	data := []byte("XBYTES")
	f, _, url, _ := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data)))
	f.write(t, filepath.Join("D", "target.txt"), "T")
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	osSymlink(filepath.Join(f.src, "D", "target.txt"), filepath.Join(f.src, "D", "art.bin"))

	err := c.Rescue(func() { db.Build("D") })
	if err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Errorf("a foreign symlink at the declared name must fail, got %v", err)
	}
}

func TestDirtyBuild_ExtDep_DeletedLinkReplaced(t *testing.T) {
	data := []byte("HEALBYTES")
	f, cache, url, hits := extFixture(t, &data)
	pin := pinOf(data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pin, "large = true"))
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	db.Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	c.Check(os.Remove(placed))

	f.dirtyBuilder().Build("D")

	if target := c.Check2(os.Readlink(placed)); target != cache.EntryPath(pin) {
		t.Errorf("deleted link must be re-placed, targets %q", target)
	}
	if *hits != 1 {
		t.Errorf("re-place must not re-download: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_DeletedCacheEntryRefetched(t *testing.T) {
	data := []byte("REFETCHBYTES")
	f, cache, url, hits := extFixture(t, &data)
	pin := pinOf(data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pin, "large = true"))
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	db.Build("D")

	entry := cache.EntryPath(pin)
	c.Check(os.Chmod(entry, 0o644)) // cache entries are read-only
	c.Check(os.Remove(entry))

	f.dirtyBuilder().Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	if target := c.Check2(os.Readlink(placed)); target != entry {
		t.Errorf("re-fetched link targets %q, want %q", target, entry)
	}
	if got := c.Check2(os.ReadFile(placed)); !bytes.Equal(got, data) {
		t.Errorf("re-fetched artifact through link = %q, want %q", got, data)
	}
	if *hits != 2 {
		t.Errorf("deleted cache entry must be fetched again: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_PlacesCopyAndSkips(t *testing.T) {
	data := []byte("COPYBYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data)))
	f.dirtyBuilder().Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	info := c.Check2(os.Lstat(placed))
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("a default (non-large) artifact must be placed as a real file, not a link")
	}
	if info.Mode()&0o200 == 0 {
		t.Error("a placed copy must be writable (cache read-only must not propagate)")
	}
	if got := c.Check2(os.ReadFile(placed)); !bytes.Equal(got, data) {
		t.Errorf("placed bytes = %q, want %q", got, data)
	}

	f.dirtyBuilder().Build("D")
	if *hits != 1 {
		t.Errorf("repeat dirty build hit the network: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_StalePinCopyReplaced(t *testing.T) {
	data := []byte("V1BYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data)))
	f.dirtyBuilder().Build("D")

	dataV2 := []byte("V2BYTES")
	data = dataV2 // switch the served payload
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(dataV2)))

	f.dirtyBuilder().Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	if got := c.Check2(os.ReadFile(placed)); !bytes.Equal(got, dataV2) {
		t.Errorf("stale-pin copy must be replaced, holds %q", got)
	}
	if *hits != 2 {
		t.Errorf("server hits = %d, want 2 (one per pin)", *hits)
	}
}

// The TODO scenario in dirty mode: the placed link looks current under the stale pin, but the
// edited url must still pass the U-link gate.
func TestDirtyBuild_ExtDep_LargeChangedURLReverified(t *testing.T) {
	data := []byte("V1BYTES")
	f, cache, url, hits := extFixture(t, &data)
	oldPin := pinOf(data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, oldPin, "large = true"))
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	db.Build("D")

	data = []byte("V2BYTES")
	newPin := pinOf(data)
	newURL := url + "?version=2"
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(newURL, oldPin, "large = true"))
	err := c.Rescue(func() { f.dirtyBuilder().Build("D") })
	if err == nil {
		t.Fatal("a changed URL must be verified even when the old artifact is already placed")
	}
	if msg := err.Error(); !strings.Contains(msg, oldPin) || !strings.Contains(msg, newPin) {
		t.Errorf("mismatch error must name expected and computed hashes, got %q", msg)
	}
	if !cache.Contains(newPin) {
		t.Error("the mismatching download must be retained under its computed hash")
	}
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(newURL, newPin, "large = true"))
	f.dirtyBuilder().Build("D")
	if target := c.Check2(os.Readlink(filepath.Join(f.src, "D", "art.bin"))); target != cache.EntryPath(newPin) {
		t.Errorf("re-pinned placement targets %q, want %q", target, cache.EntryPath(newPin))
	}
	if *hits != 2 {
		t.Errorf("server hits = %d, want one verification per URL and no re-pin download", *hits)
	}
}

func TestDirtyBuild_ExtDep_MutatedCopyFails(t *testing.T) {
	data := []byte("PRISTINE")
	f, _, url, _ := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data)))
	f.dirtyBuilder().Build("D")

	// A script-mutated copy hashes to nothing in the cache, so it must never be silently replaced.
	placed := filepath.Join(f.src, "D", "art.bin")
	c.Check(os.WriteFile(placed, []byte("TAMPERED"), 0o644))

	err := c.Rescue(func() { f.dirtyBuilder().Build("D") })
	if err == nil || !strings.Contains(err.Error(), "foreign or modified") {
		t.Errorf("a mutated copy must fail as foreign, got %v", err)
	}
}

func TestDirtyBuild_ExtDep_DeletedCopyReplaced(t *testing.T) {
	data := []byte("HEALBYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pinOf(data)))
	f.dirtyBuilder().Build("D")

	placed := filepath.Join(f.src, "D", "art.bin")
	c.Check(os.Remove(placed))

	f.dirtyBuilder().Build("D")

	if got := c.Check2(os.ReadFile(placed)); !bytes.Equal(got, data) {
		t.Errorf("deleted copy must be re-placed, holds %q", got)
	}
	if *hits != 1 {
		t.Errorf("re-place must not re-download: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_FlagFlipReplacesPlacement(t *testing.T) {
	data := []byte("FLIPBYTES")
	f, cache, url, hits := extFixture(t, &data)
	pin := pinOf(data)
	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pin))
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	db.Build("D")
	placed := filepath.Join(f.src, "D", "art.bin")

	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pin, "large = true"))
	f.dirtyBuilder().Build("D")
	if target := c.Check2(os.Readlink(placed)); target != cache.EntryPath(pin) {
		t.Errorf("flip to large must re-place as a cache link, targets %q", target)
	}

	f.write(t, filepath.Join("D", "BUFA"), "cmd = false\n"+extToml(url, pin))
	f.dirtyBuilder().Build("D")
	if info := c.Check2(os.Lstat(placed)); info.Mode()&os.ModeSymlink != 0 {
		t.Error("flip back must re-place as a real file")
	}
	if got := c.Check2(os.ReadFile(placed)); !bytes.Equal(got, data) {
		t.Errorf("flipped-back copy = %q, want %q", got, data)
	}
	if *hits != 1 {
		t.Errorf("flag flips re-downloaded: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_HealSkipsScript(t *testing.T) {
	data := []byte("HEALSKIPBYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winScript, unixScript)+extToml(url, pinOf(data)))
	f.write(t, filepath.Join("D", "own.txt"), "OWN")
	f.dirtyBuilder().Build("D")
	if runs := f.countRuns(t); runs != 1 {
		t.Fatalf("first dirty build ran script %d times, want 1", runs)
	}

	c.Check(os.Remove(filepath.Join(f.src, "D", "art.bin")))
	f.dirtyBuilder().Build("D")

	if runs := f.countRuns(t); runs != 1 {
		t.Errorf("script ran %d times, want 1 (heal alone must not re-run it)", runs)
	}
	if got := c.Check2(os.ReadFile(filepath.Join(f.src, "D", "art.bin"))); !bytes.Equal(got, data) {
		t.Errorf("healed artifact = %q, want %q", got, data)
	}
	if *hits != 1 {
		t.Errorf("heal must not re-download: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_ForceRunsScriptPastHeal(t *testing.T) {
	data := []byte("HEALFORCEBYTES")
	f, _, url, hits := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winScript, unixScript)+extToml(url, pinOf(data)))
	f.write(t, filepath.Join("D", "own.txt"), "OWN")
	f.dirtyBuilder().Build("D")

	c.Check(os.Remove(filepath.Join(f.src, "D", "art.bin")))
	b := f.dirtyBuilder()
	b.ForceRebuild = Runtime.ScopeAll
	b.Build("D")

	if runs := f.countRuns(t); runs != 2 {
		t.Errorf("script ran %d times, want 2 (a forced rebuild must not heal-skip)", runs)
	}
	if got := c.Check2(os.ReadFile(filepath.Join(f.src, "D", "art.bin"))); !bytes.Equal(got, data) {
		t.Errorf("healed artifact = %q, want %q", got, data)
	}
	if *hits != 1 {
		t.Errorf("heal must not re-download: %d hits", *hits)
	}
}

func TestDirtyBuild_ExtDep_HealWithSourceChangeRunsScript(t *testing.T) {
	data := []byte("HEALBUILDBYTES")
	f, _, url, _ := extFixture(t, &data)
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winScript, unixScript)+extToml(url, pinOf(data)))
	f.write(t, filepath.Join("D", "own.txt"), "OWN")
	f.dirtyBuilder().Build("D")

	c.Check(os.Remove(filepath.Join(f.src, "D", "art.bin")))
	f.write(t, filepath.Join("D", "own.txt"), "OWN-v2")
	f.dirtyBuilder().Build("D")

	if runs := f.countRuns(t); runs != 2 {
		t.Errorf("script ran %d times, want 2 (a real change must still build)", runs)
	}
	if got := f.readSrc(t, "D/out.txt"); got != "OWN-v2" {
		t.Errorf("output = %q, want the rebuilt OWN-v2", got)
	}
}
