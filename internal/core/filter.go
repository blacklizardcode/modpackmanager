package core

import (
	"strings"

	"modpackmanager/internal/markup"
	"modpackmanager/internal/model"
)

// ModFilter is a parsed local search query for the mod list.
//
// Free text matches name, slug, summary (Markdown stripped), author, file
// name and group. Filters: side:client|server|both|unknown,
// source:modrinth|curseforge|local, group:<name>, type:mod|resourcepack|shader.
// Quote values with spaces: group:"World gen".
type ModFilter struct {
	Terms   []string
	Side    model.Side
	Source  model.Source
	Group   string
	Type    string
	present bool
}

// ParseFilter parses a query string.
func ParseFilter(q string) ModFilter {
	var f ModFilter
	for _, tok := range splitQuery(q) {
		k, v, ok := strings.Cut(tok, ":")
		if ok && v != "" {
			switch strings.ToLower(k) {
			case "side":
				f.Side = model.ParseSide(v)
				if f.Side == model.SideUnknown && !strings.EqualFold(v, "unknown") {
					f.Side = ""
				}
				f.present = true
				continue
			case "source", "from":
				switch strings.ToLower(v) {
				case "modrinth", "mr":
					f.Source = model.SourceModrinth
				case "curseforge", "cf":
					f.Source = model.SourceCurseForge
				case "local", "other":
					f.Source = model.SourceLocal
				}
				f.present = true
				continue
			case "group":
				f.Group = strings.ToLower(v)
				f.present = true
				continue
			case "type":
				f.Type = strings.ToLower(v)
				f.present = true
				continue
			}
		}
		f.Terms = append(f.Terms, strings.ToLower(tok))
		f.present = true
	}
	return f
}

func splitQuery(q string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Empty reports whether the filter matches everything.
func (f ModFilter) Empty() bool { return !f.present }

// Match reports whether a mod passes the filter.
func (f ModFilter) Match(p *model.Pack, m *model.Mod) bool {
	if f.Side != "" {
		s := m.Side
		if s == "" {
			s = model.SideUnknown
		}
		if s != f.Side {
			return false
		}
	}
	if f.Source != "" && m.PrimarySource() != f.Source {
		if !(f.Source == model.SourceCurseForge && m.CurseForge != nil) {
			return false
		}
	}
	if f.Group != "" && !strings.Contains(strings.ToLower(p.GroupName(m.GroupID)), f.Group) {
		return false
	}
	if f.Type != "" {
		t := m.ProjectType
		if t == "" {
			t = "mod"
		}
		if !strings.HasPrefix(t, f.Type) {
			return false
		}
	}
	if len(f.Terms) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{
		m.DisplayName(), m.Slug, markup.MarkdownToPlain(m.Summary), m.Author, m.File.Name(), p.GroupName(m.GroupID), m.Notes,
	}, "\n"))
	for _, t := range f.Terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}
