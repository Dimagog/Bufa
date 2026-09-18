package Store

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

type CheckStats struct {
	CheckedContent    int // D<hash> content dirs re-hashed
	CheckedLinks      int // ∕<path> and B<combined> index links resolved
	CorruptContent    int // D entries whose content hash mismatches the name
	BadLinks          int // index links that dangle or target the wrong kind of entry
	UnexpectedEntries int // names with no valid prefix, or wrong-kind squatters on index-link / cache-dir names
	RemovedEntries    int // entries deleted (fix mode only)
}

func (st CheckStats) Ok() bool {
	return st.CorruptContent+st.BadLinks+st.UnexpectedEntries == 0
}

// cmd/bufa passes every fix run unconditionally: whatever is reported here MUST be removed under fix.
func (s *Store) Check(out io.Writer, fix bool, allowedFiles ...string) CheckStats {
	// Verification must not trust hash names
	c.Require(Hashing.SafeHashing, "Store.Check requires SafeHashing")

	cc := &checkContext{store: s, fix: fix, out: out}
	cc.checkTopLevel(allowedFiles)
	cc.checkContentRoot(InRoot, false)
	cc.checkContentRoot(OutRoot, true)
	cc.checkUserRoot()
	return cc.st
}

// Same rule as Nuke's refusal, applied one entry at a time.
func (cc *checkContext) checkTopLevel(allowedFiles []string) {
	owned := ownedTopLevel(allowedFiles)
	for _, info := range cc.store.listInfos(".") {
		if !owned(info) {
			cc.handleUnexpected(".", info.Name(), "")
		}
	}
}

type checkContext struct {
	store *Store
	fix   bool
	out   io.Writer
	st    CheckStats
}

// An unexpected entry may be a file, dir, or symlink: RemoveAll deletes any of them, a symlink as the link
// itself, never its referent.
func (cc *checkContext) handleUnexpected(root, name, detail string) {
	entry := path.Join(root, name)
	cc.st.UnexpectedEntries++
	fmt.Fprintf(cc.out, "UNEXPECTED ENTRY: '%s'%s\n", entry, detail)
	if cc.fix {
		c.Check(cc.store.fs.RemoveAll(filepath.Join(root, name)))
		cc.st.RemovedEntries++
		fmt.Fprintf(cc.out, "FIXED: removed unexpected entry '%s'\n", entry)
	}
}

func (cc *checkContext) handleBadLink(root, name, problem string) {
	entry := path.Join(root, name)
	cc.st.BadLinks++
	fmt.Fprintf(cc.out, "BAD LINK: '%s' %s\n", entry, problem)
	if cc.fix {
		cc.store.removeLink(root, name)
		cc.st.RemovedEntries++
		fmt.Fprintf(cc.out, "FIXED: removed bad link '%s'\n", entry)
	}
}

// Shape only; an unlinked P… is gc's concern, not check's.
func (cc *checkContext) checkUserRoot() {
	infos := cc.store.listInfos(UserRoot)
	present := Util.NewSet[string]()
	for _, info := range infos {
		if name := info.Name(); Hashing.IsValidPathHash(name) && info.IsDir() {
			present.Add(name)
		}
	}
	for _, info := range infos {
		name := info.Name()
		if Hashing.IsValidPathHash(name) {
			if !info.IsDir() {
				cc.handleUnexpected(UserRoot, name, " is not a directory")
			}
		} else if strings.HasPrefix(name, PathLinkPrefix) {
			cc.st.CheckedLinks++
			target := cc.store.GetLinkTarget(UserRoot, name)
			if target == "" {
				cc.handleUnexpected(UserRoot, name, " is not a symlink")
			} else {
				dir := pathFromLinkName(name)
				expected := cacheDirName(dir)
				if target != expected {
					cc.handleBadLink(UserRoot, name, fmt.Sprintf("target '%s' is not the cache dir of '%s' ('%s')",
						target, dir, expected))
				} else if !present.Contains(target) {
					cc.handleBadLink(UserRoot, name, fmt.Sprintf("target '%s' does not exist", target))
				}
			}
		} else {
			cc.handleUnexpected(UserRoot, name, "")
		}
	}
}

func (cc *checkContext) checkContentRoot(root string, viaBuildLink bool) {
	infos := cc.store.listInfos(root)
	if len(infos) == 0 {
		return
	}

	present := Util.NewSet[string]()
	for _, info := range infos {
		name := info.Name()
		if Hashing.IsValidDirHash(name) || (viaBuildLink && Hashing.IsValidBuildHash(name)) {
			present.Add(name)
		}
	}

	// out/'s B tier resolves first, so ∕ -> B -> D never depends on enumeration order.
	var removed Util.Set[string] // entries fix deleted — a ∕ rooted at a removed B is dangling now
	if cc.fix {
		removed = Util.NewSet[string]()
	}
	checkLink := func(name string, isWantedKind func(string) bool, wantPrefix string) string {
		cc.st.CheckedLinks++
		target := cc.store.GetLinkTarget(root, name)
		if target == "" {
			cc.handleUnexpected(root, name, " is not a symlink")
		} else if !isWantedKind(target) {
			cc.handleBadLink(root, name, fmt.Sprintf("target '%s' is not a '%s' entry", target, wantPrefix))
		} else if !present.Contains(target) || removed.Contains(target) {
			cc.handleBadLink(root, name, fmt.Sprintf("target '%s' does not exist", target))
		} else {
			return target
		}
		if cc.fix {
			removed.Add(name)
		}
		return ""
	}
	pathReferrers := map[string][]string{}  // D<hash> -> ∕ link names referencing it (through B for out/)
	buildReferrers := map[string][]string{} // D<hash> -> B link names referencing it (out/ only)
	buildTargets := map[string]string{}     // B<combined> -> D<hash> (out/ only)
	if viaBuildLink {
		for _, info := range infos {
			name := info.Name()
			if !Hashing.IsValidBuildHash(name) {
				continue
			}
			if target := checkLink(name, Hashing.IsValidDirHash, Hashing.DirPrefix); target != "" {
				buildTargets[name] = target
				buildReferrers[target] = append(buildReferrers[target], name)
			}
		}
	}
	pathWantKind := Hashing.IsValidDirHash
	pathWantPrefix := Hashing.DirPrefix
	if viaBuildLink {
		pathWantKind = Hashing.IsValidBuildHash
		pathWantPrefix = Hashing.BuildPrefix
	}
	for _, info := range infos {
		name := info.Name()
		switch {
		case strings.HasPrefix(name, PathLinkPrefix): // validated below
		case Hashing.IsValidDirHash(name), viaBuildLink && Hashing.IsValidBuildHash(name):
			continue // content, checked in pass 2; B links resolved above
		default:
			cc.handleUnexpected(root, name, "")
			continue
		}
		target := checkLink(name, pathWantKind, pathWantPrefix)
		if target == "" {
			continue
		}
		content := target
		if viaBuildLink {
			content = buildTargets[target] // a ∕ rooted at a bad B resolves to "" (reported above)
		}
		if content != "" {
			pathReferrers[content] = append(pathReferrers[content], name)
		}
	}

	for _, info := range infos {
		name := info.Name()
		if !Hashing.IsValidDirHash(name) {
			continue
		}
		cc.st.CheckedContent++
		if info.IsDir() {
			// A dangling or cycling symlink panics the hash walk; report it and keep verifying.
			var calculatedHash string
			hashErr := c.Rescue(func() { calculatedHash = Hashing.HashDir(cc.store.fs, filepath.Join(root, name)) })
			if hashErr != nil {
				fmt.Fprintf(cc.out, "CORRUPT: '%s' cannot be hashed: %v (%s)\n",
					root+"/"+name, hashErr, describeSources(pathReferrers[name]))
			} else if calculatedHash != name {
				fmt.Fprintf(cc.out, "CORRUPT: '%s' content hashes to '%s' (%s)\n",
					root+"/"+name, calculatedHash, describeSources(pathReferrers[name]))
			} else {
				continue
			}
		} else {
			fmt.Fprintf(cc.out, "CORRUPT: '%s' is not a directory\n", root+"/"+name)
		}
		cc.st.CorruptContent++
		if cc.fix {
			cc.fixCorrupt(root, name, slices.Concat(pathReferrers[name], buildReferrers[name]))
		}
	}
}

// Links go first: a crash mid-fix then leaves an orphan for gc, not links at deleted content.
func (cc *checkContext) fixCorrupt(root, name string, links []string) {
	for _, link := range links {
		cc.store.removeLink(root, link)
		fmt.Fprintf(cc.out, "FIXED: removed link '%s'\n", root+"/"+link)
	}
	c.Check(cc.store.fs.RemoveAll(filepath.Join(root, name)))
	cc.st.RemovedEntries += 1 + len(links)
	// "removed", not "removed dir": the corrupt entry may be a squatting file.
	fmt.Fprintf(cc.out, "FIXED: removed '%s'\n", root+"/"+name)
}

func describeSources(linkNames []string) string {
	if len(linkNames) == 0 {
		return "dangling: no path link references it"
	}
	dirs := make([]string, len(linkNames))
	for i, name := range linkNames {
		dirs[i] = "'" + pathFromLinkName(name) + "'"
	}
	slices.Sort(dirs)
	return "source dirs: " + strings.Join(dirs, ", ")
}
