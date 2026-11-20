package formats

import (
	"archive/zip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"modpackmanager/internal/jarmeta"
	"modpackmanager/internal/model"
)

// LocalMod makes a mod entry for a jar the pack stores itself (added from
// disk, or found in an imported pack's overrides). installPath is where the
// jar goes in the instance, e.g. mods/mymod.jar; info may be nil.
func LocalMod(installPath string, data []byte, info *jarmeta.Info) *model.Mod {
	m := &model.Mod{
		ID:          model.NewID(),
		ProjectType: "mod",
		Side:        model.SideUnknown,
		File:        model.File{Path: installPath, Data: data},
	}
	fillFileHashes(&m.File)
	if info != nil {
		m.Name = info.Name
		if m.Name == "" {
			m.Name = info.ID
		}
		m.Summary = info.Description
		m.Author = strings.Join(info.Authors, ", ")
		if info.Side != "" {
			m.Side = info.Side
		}
		if info.ID != "" || info.Version != "" || len(info.Loaders) > 0 {
			m.Local = &model.LocalRef{ModID: info.ID, Version: info.Version, Loaders: info.Loaders}
		}
	}
	if m.Name == "" {
		m.Name = m.DisplayName() // the file name without .jar
	}
	return m
}

// fillFileHashes sets size and hashes from the stored bytes.
func fillFileHashes(f *model.File) {
	s1 := sha1.Sum(f.Data)
	s5 := sha512.Sum512(f.Data)
	f.SHA1, f.SHA512, f.Size = hex.EncodeToString(s1[:]), hex.EncodeToString(s5[:]), int64(len(f.Data))
}

// promoteJars turns jars that sit directly in an overrides mods/ folder into
// mod entries, so they show up in the mod list like any other mod. The pack
// keeps the files (File.Data) and exports put them back into the overrides.
// Anything that isn't a readable jar, or that clashes with a mod the pack
// already lists, stays an override file.
func promoteJars(p *model.Pack) {
	var keep []*model.OverrideFile
	for _, o := range p.Overrides {
		if m := modFromOverride(p, o); m != nil && p.AddMod(m) {
			continue
		}
		keep = append(keep, o)
	}
	p.Overrides = keep
}

func modFromOverride(p *model.Pack, o *model.OverrideFile) *model.Mod {
	if path.Dir(o.Path) != "mods" || !strings.EqualFold(path.Ext(o.Path), ".jar") {
		return nil
	}
	for _, m := range p.Mods {
		if strings.EqualFold(m.File.Path, o.Path) {
			return nil // the manifest installs a file at the same place
		}
	}
	info, err := jarmeta.Read(o.Data)
	if err != nil {
		return nil
	}
	m := LocalMod(o.Path, o.Data, info)
	switch o.Side {
	case model.SideClient, model.SideServer:
		// client-overrides/ and server-overrides/ are the author's choice.
		m.Side, m.SideLocked = o.Side, true
	default:
		// overrides/ installs on both sides, unless the jar says otherwise.
		if m.Side == model.SideUnknown {
			m.Side = model.SideBoth
		}
	}
	return m
}

// filesDir holds the jars an .mpm stores, as files/<sha1>/<file name>.
const filesDir = "files/"

func storedFileName(m *model.Mod) string {
	return filesDir + strings.ToLower(m.File.SHA1) + "/" + path.Base(strings.ReplaceAll(m.File.Path, "\\", "/"))
}

// checkStored makes sure a stored file's hashes describe its bytes before
// they are written: a mismatch would leave a pack that can't be read back.
func checkStored(m *model.Mod) error {
	if m.File.SHA1 == "" {
		fillFileHashes(&m.File)
		return nil
	}
	s := sha1.Sum(m.File.Data)
	if !strings.EqualFold(hex.EncodeToString(s[:]), m.File.SHA1) {
		return fmt.Errorf("%s: stored file doesn't match its SHA-1", m.DisplayName())
	}
	return nil
}

// readStoredFiles collects files/<sha1>/<name> entries, checked against the
// hash in their path, keyed by lower-case SHA-1.
func readStoredFiles(zr *zip.Reader) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, filesDir) || f.FileInfo().IsDir() {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(name, filesDir), "/")
		if len(parts) != 2 || parts[1] == "" || !isSHA1(parts[0]) {
			continue // not ours; it is dropped on the next save
		}
		data, err := readAll(f)
		if err != nil {
			return nil, err
		}
		sum := sha1.Sum(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), parts[0]) {
			return nil, fmt.Errorf("%s is damaged (its SHA-1 doesn't match)", name)
		}
		out[strings.ToLower(parts[0])] = data
	}
	return out, nil
}

func isSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
