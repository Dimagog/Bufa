package ArtifactCache

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

func pinOf(data []byte) string { return Hashing.FilePrefix + Hashing.HashBytes(data) }

func serve(t *testing.T, data []byte) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		c.Check2(w.Write(data))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestUrlLinkName_Encoding(t *testing.T) {
	got := urlLinkName(`https://ex.com:8080/a?b=c*<d>|"e\f`)
	want := "https꞉∕∕ex.com꞉8080∕a？b=c∗＜d＞∣＂e⧵f"
	if got != want {
		t.Errorf("urlLinkName() = %q, want %q", got, want)
	}
}

func TestUrlLinkName_OverlongTrimsAndAppendsHash(t *testing.T) {
	url := "https://ex.com/" + strings.Repeat("x", maxUrlNameLen)
	got := urlLinkName(url)
	if len(got) != maxUrlNameLen {
		t.Errorf("len(urlLinkName(overlong)) = %d, want %d", len(got), maxUrlNameLen)
	}
	if !strings.HasPrefix(got, "https꞉∕∕ex.com∕xxx") {
		t.Errorf("trimmed name must keep the encoded url prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "_"+Hashing.HashUrl(url)) {
		t.Errorf("trimmed name must end with _U<hash>, got %q", got)
	}
}

// The trim point may land mid-rune: every '/' encodes to a 3-byte '∕'.
func TestUrlLinkName_OverlongTrimKeepsRuneBoundary(t *testing.T) {
	url := "http:" + strings.Repeat("/", maxUrlNameLen)
	got := urlLinkName(url)
	if !utf8.ValidString(got) {
		t.Errorf("trimmed name must stay valid UTF-8, got %q", got)
	}
	if len(got) > maxUrlNameLen {
		t.Errorf("len = %d, want <= %d", len(got), maxUrlNameLen)
	}
	if !strings.HasSuffix(got, "_"+Hashing.HashUrl(url)) {
		t.Errorf("trimmed name must end with _U<hash>, got %q", got)
	}
}

func TestCacheDir_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUFA_GLOBAL_CACHE_DIR", dir)
	if got, want := GetCacheDir(), filepath.Join(dir, "bufa"); got != want {
		t.Errorf("GetCacheDir() = %q, want the 'bufa' subdir of BUFA_GLOBAL_CACHE_DIR %q", got, want)
	}
}

func TestEnsure_FetchVerifyThenHit(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("artifact-bytes")
	srv, hits := serve(t, data)
	ac := New(filepath.Join(t.TempDir(), "cache")) // an absent cache dir is created on first fetch
	pin := pinOf(data)

	var out bytes.Buffer
	p := ac.Ensure(srv.URL+"/art.bin", pin, &out)

	if p != ac.EntryPath(pin) {
		t.Errorf("Ensure path = %q, want %q", p, ac.EntryPath(pin))
	}
	if got := c.Check2(os.ReadFile(p)); !bytes.Equal(got, data) {
		t.Errorf("cached bytes = %q, want %q", got, data)
	}
	if !strings.Contains(out.String(), "Downloading "+srv.URL) {
		t.Errorf("fetch must print the Downloading line, got %q", out.String())
	}
	if info := c.Check2(os.Stat(p)); info.Mode()&0o200 != 0 {
		t.Errorf("cache entry mode %v, want read-only", info.Mode())
	}
	if got := c.Check2(os.Readlink(filepath.Join(ac.Dir(), urlLinkName(srv.URL+"/art.bin")))); got != pin {
		t.Errorf("url link target = %q, want %q", got, pin)
	}

	var out2 bytes.Buffer
	ac.Ensure(srv.URL+"/art.bin", pin, &out2)
	if *hits != 1 {
		t.Errorf("server hits = %d after a cache hit, want 1", *hits)
	}
	if out2.Len() != 0 {
		t.Errorf("cache hit must print nothing, got %q", out2.String())
	}
	entries := c.Check2(os.ReadDir(ac.Dir()))
	if len(entries) != 2 || entries[0].Name() != pin || entries[1].Name() != urlLinkName(srv.URL+"/art.bin") {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("cache root = %v, want exactly the entry and its url link (no staging leftovers)", names)
	}
}

func TestEnsure_MismatchFailsButAdmitsComputed(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	srv, hits := serve(t, []byte("actual-bytes"))
	ac := New(t.TempDir())
	wrongPin := pinOf([]byte("expected-bytes"))
	url := srv.URL + "/art.bin"

	var out bytes.Buffer
	err := c.Rescue(func() { ac.Ensure(url, wrongPin, &out) })
	if err == nil {
		t.Fatal("hash mismatch must fail the fetch")
	}
	computed := pinOf([]byte("actual-bytes"))
	if msg := err.Error(); !strings.Contains(msg, wrongPin) || !strings.Contains(msg, computed) {
		t.Errorf("mismatch error must name expected and computed hashes, got %q", msg)
	}
	if ac.Contains(wrongPin) {
		t.Error("nothing may enter under the expected pin")
	}
	if !ac.Contains(computed) {
		t.Error("the download must be admitted under its computed hash")
	}

	ac.Ensure(url, computed, &out) // the free re-pin
	if *hits != 1 {
		t.Errorf("server hits = %d after re-pinning the computed hash, want 1", *hits)
	}
}

// A wrong pin re-downloads on every retry (indistinguishable from a re-pinned mutable url); the
// repeat names the cached earlier download so the user fixes the pin instead of waiting for new content.
func TestEnsure_RepeatMismatchHintsFixThePin(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	srv, hits := serve(t, []byte("actual-bytes"))
	ac := New(t.TempDir())
	wrongPin := pinOf([]byte("expected-bytes"))
	url := srv.URL + "/art.bin"
	const hint = "url content unchanged, fix the pin"

	var out bytes.Buffer
	err := c.Rescue(func() { ac.Ensure(url, wrongPin, &out) })
	if err == nil || strings.Contains(err.Error(), hint) {
		t.Fatalf("first mismatch must fail without the repeat hint, got %v", err)
	}
	err = c.Rescue(func() { ac.Ensure(url, wrongPin, &out) })
	if err == nil || !strings.Contains(err.Error(), hint) {
		t.Fatalf("repeat mismatch must carry the fix-the-pin hint, got %v", err)
	}
	if *hits != 2 {
		t.Errorf("server hits = %d, want 2 (a wrong pin re-downloads every time)", *hits)
	}
	if !ac.Contains(pinOf([]byte("actual-bytes"))) || ac.Contains(wrongPin) {
		t.Error("the repeat must leave the cache admitted under the computed hash only")
	}
}

func TestEnsure_QueryHashSentinelFailsPrintingHash(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("bootstrap-bytes")
	srv, hits := serve(t, data)
	ac := New(t.TempDir())

	var out bytes.Buffer
	err := c.Rescue(func() { ac.Ensure(srv.URL+"/art.bin", QueryHashSentinel, &out) })
	if err == nil {
		t.Fatal("hash = \"?\" must fail the build (bootstrap contract)")
	}
	if !strings.Contains(err.Error(), pinOf(data)) {
		t.Errorf("bootstrap failure must print the computed hash, got %q", err.Error())
	}
	if !ac.Contains(pinOf(data)) {
		t.Error("bootstrap download must be admitted under its computed hash")
	}

	ac.Ensure(srv.URL+"/art.bin", pinOf(data), &out)
	if *hits != 1 {
		t.Errorf("server hits = %d after pinning the bootstrap hash, want 1 (url link recorded at bootstrap)", *hits)
	}
}

func TestEnsure_DownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	ac := New(t.TempDir())

	var out bytes.Buffer
	err := c.Rescue(func() { ac.Ensure(srv.URL+"/gone.bin", pinOf([]byte("x")), &out) })
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("failed download must surface the server status, got %v", err)
	}
}

// The TODO scenario: version bumped in the url, hash pin forgotten — the present F entry must
// not be trusted; the re-download's verification is the loud failure.
func TestEnsure_UrlEditStalePinRefetchesAndFails(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	oldData, newData := []byte("v1-bytes"), []byte("v2-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "v1") {
			c.Check2(w.Write(oldData))
		} else {
			c.Check2(w.Write(newData))
		}
	}))
	t.Cleanup(srv.Close)
	ac := New(t.TempDir())
	stalePin := pinOf(oldData)

	var out bytes.Buffer
	ac.Ensure(srv.URL+"/v1/art.bin", stalePin, &out)

	err := c.Rescue(func() { ac.Ensure(srv.URL+"/v2/art.bin", stalePin, &out) })
	if err == nil {
		t.Fatal("an edited url under a stale pin must re-verify and fail, not silently hit")
	}
	if msg := err.Error(); !strings.Contains(msg, pinOf(newData)) || !strings.Contains(msg, stalePin) {
		t.Errorf("failure must name computed and pinned hashes, got %q", msg)
	}
	if !ac.Contains(stalePin) {
		t.Error("the old verified entry must survive the failed re-verify")
	}
}

func TestEnsure_UrlEditSameContentReverifiesOnce(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("mirrored-bytes")
	srv, hits := serve(t, data)
	ac := New(t.TempDir())
	pin := pinOf(data)

	var out bytes.Buffer
	ac.Ensure(srv.URL+"/mirrors/a/art.bin", pin, &out)
	ac.Ensure(srv.URL+"/mirrors/b/art.bin", pin, &out)
	if *hits != 2 {
		t.Fatalf("server hits = %d, want 2 (a mirror move re-verifies once)", *hits)
	}
	ac.Ensure(srv.URL+"/mirrors/b/art.bin", pin, &out)
	ac.Ensure(srv.URL+"/mirrors/a/art.bin", pin, &out)
	if *hits != 2 {
		t.Errorf("server hits = %d after both urls recorded, want 2 (url links coexist)", *hits)
	}
}

// A bare F entry (crash between entry and link publish) re-verifies once, then hits.
func TestEnsure_EntryWithoutUrlLinkReverifies(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("legacy-bytes")
	srv, hits := serve(t, data)
	ac := New(t.TempDir())
	pin := pinOf(data)
	c.Check(os.MkdirAll(ac.Dir(), 0o755))
	c.Check(os.WriteFile(ac.EntryPath(pin), data, 0o644))

	var out bytes.Buffer
	ac.Ensure(srv.URL+"/art.bin", pin, &out)
	if *hits != 1 {
		t.Fatalf("server hits = %d, want 1 (an entry without a url link re-verifies)", *hits)
	}
	ac.Ensure(srv.URL+"/art.bin", pin, &out)
	if *hits != 1 {
		t.Errorf("server hits = %d after the url link is recorded, want 1", *hits)
	}
}

// Rename must replace the existing url link (os.Symlink alone refuses to overwrite).
func TestEnsure_RepinSameUrlUpdatesUrlLink(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("v1-bytes")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		c.Check2(w.Write(data))
	}))
	t.Cleanup(srv.Close)
	ac := New(t.TempDir())
	url := srv.URL + "/art.bin"

	var out bytes.Buffer
	ac.Ensure(url, pinOf(data), &out)

	data = []byte("v2-bytes")
	newPin := pinOf(data)
	ac.Ensure(url, newPin, &out)
	if got := c.Check2(os.Readlink(filepath.Join(ac.Dir(), urlLinkName(url)))); got != newPin {
		t.Errorf("url link target = %q, want re-pointed %q", got, newPin)
	}
	ac.Ensure(url, newPin, &out)
	if hits != 2 {
		t.Errorf("server hits = %d, want 2 (re-pinned url hits after one re-verify)", hits)
	}
}

func skipIfNoOSSymlinks(t *testing.T, base string) {
	t.Helper()
	target := filepath.Join(base, "probe-target")
	c.Check(os.WriteFile(target, nil, 0o644))
	if err := os.Symlink(target, filepath.Join(base, "probe-link")); err != nil {
		t.Skipf("symlinks unsupported here (Windows Developer Mode off?): %v", err)
	}
}

// A cache link keys like its materialized twin; existence is never elided.
func TestHashSrc_FoldsCacheLink(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)

	data := []byte("artifact-bytes")
	pin := pinOf(data)
	ac := New(filepath.Join(base, "cache"))
	c.Check(os.MkdirAll(ac.Dir(), 0o755))
	c.Check(os.WriteFile(ac.EntryPath(pin), data, 0o644))

	srcRoot := filepath.Join(base, "src")
	c.Check(os.MkdirAll(filepath.Join(srcRoot, "u"), 0o755))
	c.Check(os.WriteFile(filepath.Join(srcRoot, "u", "a.txt"), []byte("A"), 0o644))
	c.Check(os.Symlink(ac.EntryPath(pin), filepath.Join(srcRoot, "u", "art.bin")))

	twin := filepath.Join(base, "twin")
	c.Check(os.MkdirAll(filepath.Join(twin, "u"), 0o755))
	c.Check(os.WriteFile(filepath.Join(twin, "u", "a.txt"), []byte("A"), 0o644))
	c.Check(os.WriteFile(filepath.Join(twin, "u", "art.bin"), data, 0o644))

	s := Store.NewStore(vfs.NewMemMapFs())
	includeAll := FilterFiles.Compile([]string{"+**"})
	srcFs := vfs.NewBasePathFs(vfs.NewOsFs(), srcRoot)

	want := Hashing.HashDir(vfs.NewOsFs(), filepath.Join(twin, "u"))
	if got := s.HashSrc(srcFs, "u", includeAll, true); got != want {
		t.Errorf("HashSrc(cache link) = %q, want materialized twin key %q", got, want)
	}

	c.Check(os.Remove(ac.EntryPath(pin)))
	if err := c.Rescue(func() { s.HashSrc(srcFs, "u", includeAll, true) }); err == nil {
		t.Error("HashSrc must fail on the now-dangling cache link once the entry is gone")
	}
}
