// Command modpackmanager starts the native UI for the current desktop and
// offers a few headless commands.
//
//	modpackmanager [file]                  open the GUI (GTK on GNOME & co., Qt on KDE/Windows/macOS)
//	modpackmanager info <pack>             print pack contents
//	modpackmanager export <pack> <client|common|server|full> <mrpack|curseforge|zip> [out] [--with-common] [--embed]
//	modpackmanager convert <pack> <out.mpm>
//	modpackmanager add <pack.mpm> <file.jar|folder>... [--side client|server|both] [--offline]
//	modpackmanager version
//
// Set MPM_UI=gtk or MPM_UI=qt to force a toolkit.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"modpackmanager/internal/config"
	"modpackmanager/internal/core"
	"modpackmanager/internal/formats"
	"modpackmanager/internal/model"
	"modpackmanager/internal/version"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "info":
			exit(cmdInfo(args[1:]))
			return
		case "export":
			exit(cmdExport(args[1:]))
			return
		case "convert":
			exit(cmdConvert(args[1:]))
			return
		case "add":
			exit(cmdAdd(args[1:]))
			return
		case "version", "-v", "--version":
			fmt.Println("modpackmanager", version.String())
			return
		case "-h", "--help", "help":
			fmt.Println(usage)
			return
		}
	}
	exit(launchUI(args))
}

const usage = `usage:
  modpackmanager [file]
  modpackmanager info <pack>
  modpackmanager export <pack> <client|common|server|full> <mrpack|curseforge|zip> [out] [--with-common] [--embed]
  modpackmanager convert <pack> <out.mpm>
  modpackmanager add <pack.mpm> <file.jar|folder>... [--side client|server|both] [--offline]
  modpackmanager version`

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// PreferredUI chooses a toolkit from the environment.
func PreferredUI(goos, desktop, forced string) string {
	switch strings.ToLower(forced) {
	case "gtk", "qt":
		return strings.ToLower(forced)
	}
	if goos != "linux" && !strings.HasSuffix(goos, "bsd") {
		return "qt"
	}
	d := strings.ToLower(desktop)
	for _, qt := range []string{"kde", "plasma", "lxqt", "deepin", "trinity"} {
		if strings.Contains(d, qt) {
			return "qt"
		}
	}
	return "gtk"
}

func launchUI(args []string) error {
	want := PreferredUI(runtime.GOOS, os.Getenv("XDG_CURRENT_DESKTOP"), os.Getenv("MPM_UI"))
	order := []string{want, map[string]string{"gtk": "qt", "qt": "gtk"}[want]}
	self, _ := os.Executable()
	dir := filepath.Dir(self)
	for _, ui := range order {
		name := "modpackmanager-" + ui
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidates := []string{filepath.Join(dir, name)}
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				cmd := exec.Command(c, args...)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				return cmd.Run()
			}
		}
	}
	return fmt.Errorf("no UI binary found next to %s (build modpackmanager-gtk or modpackmanager-qt, see README)", self)
}

func cmdInfo(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf(usage)
	}
	doc, err := core.Open(args[0])
	if err != nil {
		return err
	}
	svc := core.New(config.Load().APIKey())
	if doc.NeedsEnrichment() {
		rep := svc.Enrich(context.Background(), doc.Pack, nil)
		for _, w := range rep.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}
	p := doc.Pack
	fmt.Printf("%s %s (%s)\nMinecraft %s, %s %s\n", p.Name, p.Version, doc.Kind.Label(), p.Minecraft, model.LoaderLabel(p.Loader), p.LoaderVersion)
	c := p.SideCounts()
	fmt.Printf("%d mods: %d common, %d client-only, %d server-only, %d unknown; %d override files\n\n",
		len(p.Mods), c[model.SideBoth], c[model.SideClient], c[model.SideServer], c[model.SideUnknown], len(p.Overrides))
	for _, sec := range p.Sections(nil) {
		if len(sec.Mods) == 0 {
			continue
		}
		name := "Ungrouped"
		if sec.Group != nil {
			name = sec.Group.Name
		}
		fmt.Printf("[%s]\n", name)
		for _, m := range sec.Mods {
			fmt.Printf("  %-40s %-16s %s\n", m.DisplayName(), m.Side.Label(), m.SourceLabel())
		}
	}
	return nil
}

func cmdExport(args []string) error {
	var pos []string
	opt := core.ExportOptions{}
	for _, a := range args {
		switch a {
		case "--with-common":
			opt.IncludeCommon = true
		case "--embed":
			opt.EmbedNonModrinth = true
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 3 {
		return fmt.Errorf(usage)
	}
	opt.Target, opt.Format = core.Target(pos[1]), core.Format(pos[2])
	switch opt.Target {
	case core.TargetClient, core.TargetCommon, core.TargetServer, core.TargetFull:
	default:
		return fmt.Errorf("unknown target %q", pos[1])
	}
	switch opt.Format {
	case core.FormatMrpack, core.FormatCurseForge, core.FormatZip:
	default:
		return fmt.Errorf("unknown format %q", pos[2])
	}
	doc, err := core.Open(pos[0])
	if err != nil {
		return err
	}
	svc := core.New(config.Load().APIKey())
	if doc.NeedsEnrichment() {
		svc.Enrich(context.Background(), doc.Pack, nil)
	}
	out := core.SuggestedFileName(doc.Pack, opt)
	if len(pos) > 3 {
		out = pos[3]
	}
	data, res, err := svc.Export(context.Background(), doc.Pack, opt, nil, func(stage string, d, t int) {
		fmt.Fprintf(os.Stderr, "\r%-60.60s %d/%d", stage, d, t)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	for _, s := range res.Skipped {
		fmt.Fprintln(os.Stderr, "skipped:", s)
	}
	fmt.Printf("wrote %s (%d mods, %d embedded)\n", out, res.Mods, res.Embedded)
	return nil
}

func cmdConvert(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf(usage)
	}
	doc, err := core.Open(args[0])
	if err != nil {
		return err
	}
	svc := core.New(config.Load().APIKey())
	if doc.NeedsEnrichment() {
		for _, w := range svc.Enrich(context.Background(), doc.Pack, nil).Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}
	return doc.Save(args[1])
}

// cmdAdd adds jar files (or the jars in a folder) to an .mpm pack and saves it.
func cmdAdd(args []string) error {
	var pos []string
	opt := core.JarOptions{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--offline":
			opt.Offline = true
		case a == "--side" || strings.HasPrefix(a, "--side="):
			v, ok := strings.CutPrefix(a, "--side=")
			if !ok {
				if i+1 >= len(args) {
					return fmt.Errorf("--side needs a value: client, server or both")
				}
				i++
				v = args[i]
			}
			if opt.Side = model.ParseSide(v); opt.Side == model.SideUnknown {
				return fmt.Errorf("unknown side %q (use client, server or both)", v)
			}
		case strings.HasPrefix(a, "--"):
			return fmt.Errorf("unknown option %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return fmt.Errorf(usage)
	}
	doc, err := core.Open(pos[0])
	if err != nil {
		return err
	}
	if doc.Kind != formats.KindMPM {
		return fmt.Errorf("%s isn't an .mpm file; convert it first: modpackmanager convert %s pack.mpm", pos[0], pos[0])
	}
	svc := core.New(config.Load().APIKey())
	results, rep := svc.AddJars(context.Background(), doc.Pack, core.ReadJarFiles(pos[1:]), opt)
	failed := 0
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "error: %s %s\n", r.Name, r.Err)
		case r.Existing:
			fmt.Printf("unchanged  %s (already in the pack as %s)\n", r.Name, r.Mod.DisplayName())
		default:
			how := "stored"
			if r.Linked {
				how = "linked to Modrinth"
			}
			fmt.Printf("added      %s as %s (%s, %s)\n", r.Name, r.Mod.DisplayName(), r.Mod.Side.Label(), how)
			if r.Replaced != nil {
				fmt.Printf("           replaces %s\n", r.Replaced.File.Name())
			}
			for _, w := range r.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s: %s\n", r.Name, w)
			}
		}
	}
	for _, w := range rep.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if core.JarsChanged(results) {
		if err := doc.Save(""); err != nil {
			return err
		}
		fmt.Printf("saved %s\n", doc.Path)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files were not added", failed, len(results))
	}
	return nil
}
