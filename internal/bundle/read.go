package bundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/skills"
	"github.com/SandwichLabs/slaw/internal/watches"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// item is one verified, validated entry.
type item struct {
	Entry
	Title string
	Data  []byte
	// View is the second file of a data artifact (Data is its manifest).
	View []byte
	// Files are the html/ files of an artifact that is a page (Data is its
	// manifest, View is nil).
	Files    []artifacts.PageFile
	Warnings []string
}

// Package is an opened bundle: verified and validated, not yet written
// anywhere. It holds the unpacked entries in memory (at most MaxTotalBytes).
type Package struct {
	Manifest  Manifest
	Encrypted bool
	// Warnings are what opening noticed (a truncated notebook, a connection
	// that was dropped); Slaw's own words.
	Warnings []string
	// UnsupportedKinds names the entry kinds this Slaw does not know (a bundle
	// from a newer one); those entries were verified and skipped, never written.
	UnsupportedKinds []string
	// MakerNotes are the manifest's own warnings, written by whoever made the
	// bundle (cleaned and bounded). They are shown apart from Slaw's.
	MakerNotes []string

	items []item
	conns []workspace.ConnectionDef
	// askedWrite names connections whose definition asked to be writable.
	askedWrite map[string]bool

	mu   sync.Mutex
	revs map[string]string // revision of each existing item, noted at Preview
}

// ReadFile reads a bundle from disk, refusing a file over MaxFileBytes before
// reading it.
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the person's own choice
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes, over the %d byte limit", ErrTooLarge, path, fi.Size(), MaxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if len(data) > MaxFileBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// Open decrypts (when needed), unpacks, verifies and validates a bundle. Any
// problem refuses the whole bundle: nothing is partially trusted.
func Open(data []byte, passphrase string) (*Package, error) {
	info, err := Peek(data)
	if err != nil {
		return nil, err
	}
	plain := data
	if info.Encrypted {
		if plain, err = decrypt(data, passphrase); err != nil {
			return nil, err
		}
		inner, err := Peek(plain)
		if err != nil || inner.Encrypted {
			return nil, corrupt("the decrypted content is not a bundle")
		}
	}
	p, err := unpack(plain)
	if err != nil {
		return nil, err
	}
	p.Encrypted = info.Encrypted
	return p, nil
}

func unpack(plain []byte) (*Package, error) {
	zr, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		return nil, fmt.Errorf("%w: not a readable zip archive (%w)", ErrNotBundle, err)
	}
	if len(zr.File) > MaxEntries {
		return nil, corrupt("%d entries, over the %d entry limit", len(zr.File), MaxEntries)
	}

	// Pass 1: names, modes and declared sizes, before a byte is decompressed.
	files := map[string]*zip.File{}
	var declared uint64
	for _, f := range zr.File {
		if _, _, err := classify(f.Name); err != nil && checkEntryName(f.Name) != nil {
			return nil, err // an unsafe name is refused; a safe, unknown one is judged against the manifest
		}
		if _, dup := files[f.Name]; dup {
			return nil, corrupt("entry %q appears twice", f.Name)
		}
		if mode := f.Mode(); !mode.IsRegular() {
			return nil, corrupt("entry %q is not a plain file (links and directories are refused)", f.Name)
		}
		if f.Flags&0x1 != 0 {
			return nil, corrupt("entry %q is password-protected zip content, which a bundle never is", f.Name)
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, corrupt("entry %q uses an unsupported compression method", f.Name)
		}
		limit := uint64(MaxEntryBytes)
		if f.Name == ManifestName {
			limit = maxManifestBytes
		}
		if f.UncompressedSize64 > limit {
			return nil, fmt.Errorf("%w: entry %q claims %d bytes, over the %d byte limit", ErrTooLarge, f.Name, f.UncompressedSize64, limit)
		}
		declared += f.UncompressedSize64
		if declared > MaxTotalBytes {
			return nil, fmt.Errorf("%w: the entries unpack to more than %d bytes", ErrTooLarge, MaxTotalBytes)
		}
		files[f.Name] = f
	}

	var total int64
	read := func(f *zip.File, limit int64) ([]byte, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, corrupt("entry %q cannot be opened (%v)", f.Name, err)
		}
		defer rc.Close()
		// The declared size can lie; this is the real cap.
		b, err := io.ReadAll(io.LimitReader(rc, limit+1))
		if err != nil {
			return nil, corrupt("entry %q is damaged (%v)", f.Name, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%w: entry %q is over the %d byte limit", ErrTooLarge, f.Name, limit)
		}
		total += int64(len(b))
		if total > MaxTotalBytes {
			return nil, fmt.Errorf("%w: the entries unpack to more than %d bytes", ErrTooLarge, MaxTotalBytes)
		}
		return b, nil
	}

	mf, ok := files[ManifestName]
	if !ok {
		return nil, fmt.Errorf("%w: there is no %s", ErrNotBundle, ManifestName)
	}
	mb, err := read(mf, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	var head struct {
		Kind          string `json:"kind"`
		FormatVersion int    `json:"formatVersion"`
	}
	if err := json.Unmarshal(mb, &head); err != nil {
		return nil, corrupt("%s is not valid JSON", ManifestName)
	}
	if head.Kind != Kind {
		return nil, fmt.Errorf("%w: %s says kind %q", ErrNotBundle, ManifestName, head.Kind)
	}
	switch {
	case head.FormatVersion == 1:
		return nil, ErrLegacy
	case head.FormatVersion > FormatVersion:
		return nil, fmt.Errorf("this bundle was made by a newer version of Slaw (format %d); update Slaw to open it", head.FormatVersion)
	case head.FormatVersion != FormatVersion:
		return nil, corrupt("%s has formatVersion %d", ManifestName, head.FormatVersion)
	}
	var man Manifest
	dec := json.NewDecoder(bytes.NewReader(mb))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&man); err != nil {
		return nil, corrupt("%s: %v", ManifestName, err)
	}

	// The manifest must list exactly the entries that are in the archive.
	listed := map[string]bool{}
	unknown := map[string]bool{} // listed paths of a kind this Slaw does not know
	kinds := map[string]bool{}
	for _, e := range man.Contents {
		if listed[e.Path] {
			return nil, corrupt("%s lists %q twice", ManifestName, e.Path)
		}
		listed[e.Path] = true
		if e.Path == ManifestName {
			return nil, corrupt("%s lists itself", ManifestName)
		}
		kind, id, err := classify(e.Path)
		if err != nil {
			// Only a safe path (checked on the archive above) under a kind
			// name that is not one of ours is tolerated; it is still
			// size-capped and checksummed below, then skipped.
			if !unsupportedKind(e.Kind) || inKnownArea(e.Path) {
				return nil, err
			}
			if files[e.Path] == nil {
				return nil, corrupt("%s lists %q, which is not in the archive", ManifestName, e.Path)
			}
			unknown[e.Path], kinds[string(e.Kind)] = true, true
			continue
		}
		if kind != e.Kind || id != e.ID {
			return nil, corrupt("%s: the entry for %q does not match its path", ManifestName, e.Path)
		}
		if files[e.Path] == nil {
			return nil, corrupt("%s lists %q, which is not in the archive", ManifestName, e.Path)
		}
	}
	for name := range files {
		if name != ManifestName && !listed[name] {
			return nil, corrupt("entry %q is in the archive but not in %s", name, ManifestName)
		}
	}

	man.WorkspaceName = cleanText(man.WorkspaceName, maxTextRunes)
	man.CreatedBy = cleanText(man.CreatedBy, maxTextRunes)
	man.CreatedAt = cleanText(man.CreatedAt, 40)
	p := &Package{Manifest: man, MakerNotes: cleanNotes(man.Warnings), revs: map[string]string{}}
	arts := artifactParts{}
	for _, e := range man.Contents {
		b, err := read(files[e.Path], MaxEntryBytes)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), e.SHA256) || int64(len(b)) != e.Size {
			return nil, corrupt("%q does not match its checksum", e.Path)
		}
		if unknown[e.Path] {
			continue // verified above, never parsed or written
		}
		if e.Kind == KindConnections {
			defs, asked, warnings, err := parseConnections(b)
			if err != nil {
				return nil, err
			}
			p.conns, p.askedWrite, p.Warnings = defs, asked, append(p.Warnings, warnings...)
			continue
		}
		if e.Kind == KindArtifact {
			if err := arts.add(e, b); err != nil {
				return nil, err
			}
			continue
		}
		title, warnings, err := validateItem(e.Kind, e.ID, b)
		if err != nil {
			return nil, err
		}
		p.Warnings = append(p.Warnings, warnings...)
		p.items = append(p.items, item{Entry: e, Title: cleanText(title, maxTextRunes), Data: b})
	}
	p.noteUnsupported(kinds)
	artItems, err := arts.items()
	if err != nil {
		return nil, err
	}
	p.items = append(p.items, artItems...)
	sort.Slice(p.items, func(i, j int) bool { return p.items[i].Path < p.items[j].Path })
	return p, nil
}

// Bounds on kind names taken from a manifest and on how many the warning lists.
const (
	maxUnsupportedKindRunes  = 32
	maxUnsupportedKindsShown = 10
)

var unsupportedKindRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// unsupportedKind reports a manifest kind that is a plain lowercase word and
// not one this Slaw knows, so a newer Slaw's entries can be skipped. Anything
// else (a known kind on a path it does not own, odd characters) stays an error.
func unsupportedKind(k ItemKind) bool {
	switch k {
	case KindNotebook, KindQuery, KindSkill, KindArtifact, KindSemantics, KindWatch, KindConnections, KindConnection:
		return false
	}
	return len(k) <= maxUnsupportedKindRunes && unsupportedKindRE.MatchString(string(k))
}

// inKnownArea reports a path under a top-level folder (or the file) that a
// known kind owns, other than artifacts/, which a newer Slaw may add files to.
// A bad name there is a damaged bundle, not a newer one.
func inKnownArea(p string) bool {
	top, _, _ := strings.Cut(p, "/")
	switch top {
	case notebooks.Dir, queries.Dir, skills.Dir, semantics.Dir, watches.Dir, ConnectionsName:
		return true
	}
	return false
}

// noteUnsupported records the skipped kinds and the one warning that names them.
func (p *Package) noteUnsupported(kinds map[string]bool) {
	if len(kinds) == 0 {
		return
	}
	for k := range kinds {
		p.UnsupportedKinds = append(p.UnsupportedKinds, k)
	}
	sort.Strings(p.UnsupportedKinds)
	shown, more := p.UnsupportedKinds, ""
	if len(shown) > maxUnsupportedKindsShown {
		more = fmt.Sprintf(" and %d more", len(shown)-maxUnsupportedKindsShown)
		shown = shown[:maxUnsupportedKindsShown]
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("This bundle has entries this version of Slaw doesn't know (%s%s). They were skipped; update Slaw to import them.", strings.Join(shown, ", "), more))
}
