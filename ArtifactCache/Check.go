package ArtifactCache

import (
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

// Not a const only for Unit Test's sake
var stagingGracePeriod = 5 * time.Minute

type CheckStats struct {
	CheckedEntries    int // F<hash> entries re-hashed
	CheckedLinks      int // url links resolved
	CorruptEntries    int // F entries whose content hash mismatches the name, or F-named symlinks
	BadLinks          int // url links whose F target is absent
	StagingLeftovers  int // fetch-*/url-* staging files past the grace period
	UnexpectedEntries int // anything classify doesn't recognize
	RemovedEntries    int // entries deleted (fix mode only)
}

func (st CheckStats) HasProblems() bool {
	return st.CorruptEntries+st.BadLinks+st.StagingLeftovers+st.UnexpectedEntries != 0
}

type checkContext struct {
	cache *Cache
	fix   bool
	out   io.Writer
	st    CheckStats
}

// cmd/bufa passes every fix run unconditionally: whatever is reported here MUST be removed under fix.
func (ac *Cache) Check(out io.Writer, fix bool) CheckStats {
	infos := vfsx.ReadDirIfExists(ac.fs, ".")
	if len(infos) == 0 {
		return CheckStats{}
	}

	cc := &checkContext{cache: ac, fix: fix, out: out}
	kinds := make([]entryKind, len(infos))
	linkTargets := make([]string, len(infos))
	present := Util.NewSet[string]()
	for i, info := range infos {
		kinds[i], linkTargets[i] = ac.classify(info)
		if kinds[i] == kindEntry {
			present.Add(info.Name())
		}
	}

	// Links resolve first, so a corrupt entry's fix knows every url link to take down with it.
	referrers := map[string][]string{} // F<hash> -> url link names targeting it
	for i, info := range infos {
		name := info.Name()
		switch kinds[i] {
		case kindUrlLink:
			cc.checkUrlLink(name, linkTargets[i], present, referrers)
		case kindFetchStaging, kindUrlStaging:
			cc.handleStaging(info)
		case kindForeign:
			cc.handleUnexpected(name)
		}
	}

	for i, info := range infos {
		if kinds[i] == kindEntry {
			cc.checkEntry(info.Name(), referrers[info.Name()])
		}
	}
	return cc.st
}

func (cc *checkContext) checkUrlLink(name, target string, present Util.Set[string], referrers map[string][]string) {
	if Hashing.IsValidFileHash(name) {
		// Contains follows links: this one would serve another entry's bytes under its own pin.
		cc.st.CheckedEntries++
		cc.handleCorrupt(name, "is a symlink, not a regular file", nil)
	} else {
		cc.st.CheckedLinks++
		if present.Contains(target) {
			referrers[target] = append(referrers[target], name)
		} else {
			cc.handleBadLink(name, fmt.Sprintf("target '%s' does not exist", target))
		}
	}
}

func (cc *checkContext) checkEntry(name string, urlLinks []string) {
	cc.st.CheckedEntries++
	var calculatedHash string
	hashErr := c.Rescue(func() { calculatedHash = Hashing.HashFile(cc.cache.fs, name) })
	if hashErr != nil {
		cc.handleCorrupt(name, fmt.Sprintf("cannot be hashed: %v", hashErr), urlLinks)
	} else if calculatedHash != name {
		cc.handleCorrupt(name, fmt.Sprintf("content hashes to '%s'", calculatedHash), urlLinks)
	}
}

// A young staging file may be another process's download in flight: neither reported nor removed.
func (cc *checkContext) handleStaging(info fs.FileInfo) {
	if time.Since(info.ModTime()) >= stagingGracePeriod {
		cc.st.StagingLeftovers++
		fmt.Fprintf(cc.out, "STAGING LEFTOVER: '%s' (older than %s)\n", cc.cache.EntryPath(info.Name()), stagingGracePeriod)
		cc.fixRemove(info.Name(), "staging leftover", cc.cache.fs.Remove)
	}
}

// May be a file, dir, or symlink: RemoveAll deletes a symlink as the link itself, never its referent.
func (cc *checkContext) handleUnexpected(name string) {
	cc.st.UnexpectedEntries++
	fmt.Fprintf(cc.out, "UNEXPECTED ENTRY: '%s'\n", cc.cache.EntryPath(name))
	cc.fixRemove(name, "unexpected entry", cc.cache.fs.RemoveAll)
}

func (cc *checkContext) handleBadLink(name, problem string) {
	cc.st.BadLinks++
	fmt.Fprintf(cc.out, "BAD LINK: '%s' %s\n", cc.cache.EntryPath(name), problem)
	cc.fixRemove(name, "bad link", cc.cache.fs.Remove)
}

// Links go first: a crash mid-fix then leaves an unlinked entry, never a url link vouching for nothing.
func (cc *checkContext) handleCorrupt(name, problem string, urlLinks []string) {
	entry := cc.cache.EntryPath(name)
	cc.st.CorruptEntries++
	fmt.Fprintf(cc.out, "CORRUPT: '%s' %s (%s)\n", entry, problem, describeUrlLinks(urlLinks))
	if cc.fix {
		for _, link := range urlLinks {
			cc.fixRemove(link, "url link", cc.cache.fs.Remove)
		}
		c.Check(cc.cache.fs.Remove(name))
		cc.st.RemovedEntries++
		fmt.Fprintf(cc.out, "FIXED: removed '%s'\n", entry)
	}
}

func (cc *checkContext) fixRemove(name, what string, remove func(string) error) {
	if cc.fix {
		c.Check(remove(name))
		cc.st.RemovedEntries++
		fmt.Fprintf(cc.out, "FIXED: removed %s '%s'\n", what, cc.cache.EntryPath(name))
	}
}

func describeUrlLinks(linkNames []string) string {
	if len(linkNames) == 0 {
		return "no url link references it"
	}
	quoted := make([]string, len(linkNames))
	for i, name := range linkNames {
		quoted[i] = "'" + name + "'"
	}
	slices.Sort(quoted)
	return "url links: " + strings.Join(quoted, ", ")
}
