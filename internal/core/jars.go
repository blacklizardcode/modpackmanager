package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"modpackmanager/internal/formats"
	"modpackmanager/internal/imgutil"
	"modpackmanager/internal/jarmeta"
	"modpackmanager/internal/model"
)

// ---------------------------------------------------------------------------
// Adding jar files from disk

// JarFile is a jar to add to a pack.
type JarFile struct {
	Name string // the file name, e.g. mymod-1.0.jar
	Data []byte
	Err  error // set when the file couldn't be read
}

// MaxJarSize caps one jar, like files inside a pack.
const MaxJarSize = formats.MaxOverrideFileSize

// ReadJarFiles reads jars for AddJars. A folder stands for the .jar files
// directly inside it (dropping a mods folder adds its mods). Problems are
// recorded per file rather than failing the whole batch.
func ReadJarFiles(paths []string) []JarFile {
	var out []JarFile
	for _, p := range paths {
		st, err := os.Stat(p)
		switch {
		case err != nil:
			out = append(out, JarFile{Name: filepath.Base(p), Err: unreadable(err)})
		case st.IsDir():
			jars := jarsIn(p)
			if len(jars) == 0 {
				out = append(out, JarFile{Name: filepath.Base(p), Err: errors.New("is a folder with no .jar files in it")})
			}
			for _, j := range jars {
				out = append(out, readJarFile(j))
			}
		default:
			out = append(out, readJarFile(p))
		}
	}
	return out
}

func jarsIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func readJarFile(p string) JarFile {
	f := JarFile{Name: filepath.Base(p)}
	st, err := os.Stat(p)
	switch {
	case err != nil:
		f.Err = unreadable(err)
	case !strings.EqualFold(filepath.Ext(p), ".jar"):
		f.Err = errors.New("isn't a .jar file")
	case st.Size() > MaxJarSize:
		f.Err = fmt.Errorf("is too large (%d MB)", st.Size()>>20)
	default:
		if f.Data, err = os.ReadFile(p); err != nil {
			f.Err = unreadable(err)
		}
	}
	return f
}

// unreadable phrases an os error to follow the file name ("x.jar couldn't be
// read: permission denied").
func unreadable(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("couldn't be read: %w", err)
}

// JarOptions controls AddJars.
type JarOptions struct {
	GroupID string     // group for the new mods ("" = ungrouped)
	Side    model.Side // when set, every jar gets this side, locked
	Offline bool       // don't look the files up on Modrinth
}

// JarResult says what happened to one file.
type JarResult struct {
	Name     string     // the file name
	Mod      *model.Mod // the entry in the pack: the new one, or the one that was already there
	Replaced *model.Mod // a jar the pack stored before that this one replaced (same file name or mod id)
	Existing bool       // the pack already had this file or Modrinth project; nothing changed
	Linked   bool       // the file is on Modrinth, so the pack links to it instead of storing it
	Err      error      // why the file wasn't added
	Warnings []string
}

// lookupTimeout bounds the Modrinth lookup when adding jars.
const lookupTimeout = 20 * time.Second

// PreparedJars are jars that were read and looked up but not added yet.
type PreparedJars struct {
	items []preparedJar
	opt   JarOptions
	// Report holds problems that concern the whole batch (Modrinth unreachable).
	Report
}

type preparedJar struct {
	res  JarResult
	mod  *model.Mod
	info *jarmeta.Info
}

// PrepareJars reads each jar's metadata and looks the files up on Modrinth by
// SHA-1. It doesn't touch any pack, so it can run off the UI thread; Apply
// then adds the results to a pack.
func (s *Service) PrepareJars(ctx context.Context, files []JarFile, opt JarOptions) *PreparedJars {
	pj := &PreparedJars{opt: opt}
	var candidates []*model.Mod
	for _, f := range files {
		it := preparedJar{res: JarResult{Name: f.Name}}
		if f.Err != nil {
			it.res.Err = f.Err
		} else if m, info, err := jarMod(f); err != nil {
			it.res.Err = err
		} else {
			it.mod, it.info = m, info
			candidates = append(candidates, m)
		}
		pj.items = append(pj.items, it)
	}
	if !opt.Offline && len(candidates) > 0 {
		// Don't keep the user waiting on a bad connection: without the
		// lookup the jars are simply stored.
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		if linked := s.matchModrinthMods(lctx, candidates, &pj.Report); len(linked) > 0 {
			s.enrichModrinthMods(lctx, linked, &pj.Report)
		}
		cancel()
	}
	for i := range pj.items {
		if m := pj.items[i].mod; m != nil && m.Modrinth != nil {
			pj.items[i].res.Linked = true
		}
	}
	return pj
}

func jarMod(f JarFile) (*model.Mod, *jarmeta.Info, error) {
	name := strings.TrimSpace(f.Name)
	switch {
	case !strings.EqualFold(path.Ext(name), ".jar"):
		return nil, nil, errors.New("isn't a .jar file")
	case strings.ContainsAny(name, `/\`) || formats.SafeRelPath("mods/"+name) != nil:
		return nil, nil, errors.New("has a file name that can't be used")
	case len(f.Data) == 0:
		return nil, nil, errors.New("is empty")
	}
	info, err := jarmeta.Read(f.Data)
	if err != nil {
		return nil, nil, err
	}
	return formats.LocalMod("mods/"+name, f.Data, info), info, nil
}

// Apply adds the prepared jars to the pack and reports what happened to each
// file, in the order they were given.
func (pj *PreparedJars) Apply(p *model.Pack) []JarResult {
	out := make([]JarResult, 0, len(pj.items))
	for _, it := range pj.items {
		res := it.res
		if res.Err == nil {
			pj.apply(p, it.mod, it.info, &res)
		}
		out = append(out, res)
	}
	return out
}

func (pj *PreparedJars) apply(p *model.Pack, m *model.Mod, info *jarmeta.Info, res *JarResult) {
	if dup := p.FindDuplicate(m); dup != nil {
		res.Existing, res.Mod = true, dup // the same file, or the same Modrinth project
		return
	}
	// A jar the pack stores with the same file name or mod id is an older
	// build of this one: replace it, keeping the user's group, notes and side.
	// A linked mod at the same path is a real conflict.
	var old *model.Mod
	for _, o := range p.Mods {
		if !strings.EqualFold(o.File.Path, m.File.Path) {
			continue
		}
		if !isLocal(o) {
			res.Err = fmt.Errorf("has the same file name as %s (from %s), which is already in the pack", o.DisplayName(), o.SourceLabel())
			return
		}
		old = o
	}
	if old == nil && info.ID != "" {
		for _, o := range p.Mods {
			if isLocal(o) && o.Local != nil && strings.EqualFold(o.Local.ModID, info.ID) {
				old = o
				break
			}
		}
	}
	if old != nil {
		m.GroupID, m.Notes = old.GroupID, old.Notes
		if old.SideLocked {
			m.Side, m.SideLocked = old.Side, true
		}
		p.RemoveMod(old.ID)
		res.Replaced = old
	}
	if pj.opt.GroupID != "" && p.Group(pj.opt.GroupID) != nil {
		m.GroupID = pj.opt.GroupID
	}
	if pj.opt.Side != "" {
		m.Side, m.SideLocked = pj.opt.Side, true
	}
	if w := loaderWarning(p.Loader, info.Loaders); w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	if len(info.Loaders) == 0 && m.Modrinth == nil {
		res.Warnings = append(res.Warnings, "it has no mod metadata (no fabric.mod.json or mods.toml), so it may not be a mod")
	}
	p.AddMod(m)
	res.Mod = m
}

// isLocal reports whether a mod is a stored jar rather than a linked one.
func isLocal(m *model.Mod) bool {
	return m.PrimarySource() == model.SourceLocal && len(m.File.URLs) == 0
}

// loaderWarning explains when a jar is made for another loader than the pack's.
func loaderWarning(pack string, jar []string) string {
	accepts := map[string][]string{
		"fabric":   {jarmeta.Fabric},
		"quilt":    {jarmeta.Quilt, jarmeta.Fabric},
		"forge":    {jarmeta.Forge},
		"neoforge": {jarmeta.NeoForge, jarmeta.Forge},
	}[pack]
	if accepts == nil || len(jar) == 0 {
		return ""
	}
	var labels []string
	for _, l := range jar {
		if slices.Contains(accepts, l) {
			return ""
		}
		labels = append(labels, model.LoaderLabel(l))
	}
	return fmt.Sprintf("it's a %s mod, but this pack uses %s", strings.Join(labels, "/"), model.LoaderLabel(pack))
}

// AddJars prepares and applies in one go (for the command line and tests).
func (s *Service) AddJars(ctx context.Context, p *model.Pack, files []JarFile, opt JarOptions) ([]JarResult, Report) {
	pj := s.PrepareJars(ctx, files, opt)
	return pj.Apply(p), pj.Report
}

// JarsChanged reports whether any jar was added (so the pack is now dirty).
func JarsChanged(results []JarResult) bool {
	for _, r := range results {
		if r.Err == nil && !r.Existing && r.Mod != nil {
			return true
		}
	}
	return false
}

// JarSummary turns the results into a one-line title and the problems worth
// showing (failures, warnings, files that were already there). When problems
// is empty, the title says it all.
func JarSummary(results []JarResult, rep Report) (title string, problems []string) {
	var added, linked, replaced, existing, failed []JarResult
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed = append(failed, r)
			problems = append(problems, fmt.Sprintf("%s %s.", r.Name, r.Err))
		case r.Existing:
			existing = append(existing, r)
		default:
			added = append(added, r)
			if r.Linked {
				linked = append(linked, r)
			}
			if r.Replaced != nil {
				replaced = append(replaced, r)
			}
			for _, w := range r.Warnings {
				problems = append(problems, fmt.Sprintf("%s: %s.", r.Name, w))
			}
		}
	}
	switch {
	case len(added) == 1 && len(replaced) == 1:
		title = fmt.Sprintf("Updated %s (%s replaces %s)", added[0].Mod.DisplayName(), added[0].Name, replaced[0].Replaced.File.Name())
	case len(added) == 1:
		title = "Added " + added[0].Mod.DisplayName()
		if len(linked) == 1 {
			title += " (found on Modrinth, so the pack links to it)"
		}
	case len(added) > 1:
		title = fmt.Sprintf("Added %d JAR files", len(added))
		var notes []string
		if len(replaced) > 0 {
			notes = append(notes, fmt.Sprintf("%d replacing older ones", len(replaced)))
		}
		if len(linked) > 0 {
			notes = append(notes, fmt.Sprintf("%d linked to Modrinth", len(linked)))
		}
		if len(notes) > 0 {
			title += " (" + strings.Join(notes, ", ") + ")"
		}
	case len(existing) == 1 && len(failed) == 0:
		title = existing[0].Mod.DisplayName() + " is already in the pack"
	case len(existing) > 1 && len(failed) == 0:
		title = "These files are already in the pack"
	case len(failed) == 1 && len(existing) == 0:
		return "Couldn't add " + failed[0].Name, []string{fmt.Sprintf("It %s.", failed[0].Err)}
	default:
		title = "No JAR files were added"
	}
	if len(added) > 0 || len(failed) > 0 {
		for _, r := range existing {
			problems = append(problems, fmt.Sprintf("%s is already in the pack (%s).", r.Name, r.Mod.DisplayName()))
		}
	}
	return title, append(problems, rep.Warnings...)
}

// FormatSize renders a byte count for people: 850 KB, 1.2 MB.
func FormatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+512)>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// LoaderLabels lists loader ids for people: "Fabric, Quilt".
func LoaderLabels(loaders []string) string {
	labels := make([]string, len(loaders))
	for i, l := range loaders {
		labels[i] = model.LoaderLabel(l)
	}
	return strings.Join(labels, ", ")
}

// ---------------------------------------------------------------------------
// Icons of stored jars

// JarIcon returns the icon inside a jar as PNG, scaled to fit maxSide, or nil
// when it has none that looks right in an icon slot (Forge's logoFile is
// often a wide banner).
func JarIcon(jar []byte, maxSide int) []byte {
	raw := jarmeta.Icon(jar)
	if raw == nil {
		return nil
	}
	png, notDecoded := imgutil.Normalize(raw, maxSide)
	if notDecoded {
		return nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil || cfg.Width == 0 || cfg.Height == 0 {
		return nil
	}
	if r := float64(cfg.Width) / float64(cfg.Height); r < 0.75 || r > 1.34 {
		return nil
	}
	return png
}
