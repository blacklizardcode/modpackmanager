package core

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"modpackmanager/internal/api/httpx"
)

// Endpoints for loader metadata (overridable in tests).
var (
	FabricMetaURL   = "https://meta.fabricmc.net/v2/versions/loader"
	QuiltMetaURL    = "https://meta.quiltmc.org/v3/versions/loader"
	NeoForgeMetaURL = "https://maven.neoforged.net/releases/net/neoforged/neoforge/maven-metadata.xml"
	ForgePromosURL  = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
)

// MinecraftVersions lists release versions, newest first.
func (s *Service) MinecraftVersions(ctx context.Context, includeSnapshots bool) ([]string, error) {
	tags, err := s.MR.GameVersions(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].Date.After(tags[j].Date) })
	var out []string
	for _, t := range tags {
		if t.VersionType == "release" || includeSnapshots {
			out = append(out, t.Version)
		}
	}
	return out, nil
}

// LoaderVersions lists loader versions compatible with a Minecraft version,
// newest/recommended first.
func LoaderVersions(ctx context.Context, loader, mc string) ([]string, error) {
	switch loader {
	case "fabric", "quilt":
		u := FabricMetaURL
		if loader == "quilt" {
			u = QuiltMetaURL
		}
		var out []struct {
			Version string `json:"version"`
			Stable  *bool  `json:"stable"`
		}
		if err := httpx.DoJSON(ctx, nil, "GET", u, nil, nil, &out); err != nil {
			return nil, err
		}
		var stable, other []string
		for _, v := range out {
			if loader == "quilt" && strings.Contains(v.Version, "-") {
				other = append(other, v.Version)
				continue
			}
			if v.Stable == nil || *v.Stable {
				stable = append(stable, v.Version)
			} else {
				other = append(other, v.Version)
			}
		}
		return append(stable, other...), nil
	case "neoforge":
		data, err := getRaw(ctx, NeoForgeMetaURL)
		if err != nil {
			return nil, err
		}
		var meta struct {
			Versions []string `xml:"versioning>versions>version"`
		}
		if err := xml.Unmarshal(data, &meta); err != nil {
			return nil, err
		}
		prefix := neoForgePrefix(mc)
		var out []string
		for i := len(meta.Versions) - 1; i >= 0; i-- {
			v := meta.Versions[i]
			if prefix == "" || strings.HasPrefix(v, prefix) {
				out = append(out, v)
			}
		}
		// Prefer stable (non -beta) builds first.
		sort.SliceStable(out, func(i, j int) bool {
			return !strings.Contains(out[i], "beta") && strings.Contains(out[j], "beta")
		})
		return out, nil
	case "forge":
		var promos struct {
			Promos map[string]string `json:"promos"`
		}
		if err := httpx.DoJSON(ctx, nil, "GET", ForgePromosURL, nil, nil, &promos); err != nil {
			return nil, err
		}
		var out []string
		if v, ok := promos.Promos[mc+"-recommended"]; ok {
			out = append(out, v)
		}
		if v, ok := promos.Promos[mc+"-latest"]; ok && (len(out) == 0 || out[0] != v) {
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown loader %q", loader)
}

// neoForgePrefix maps MC 1.21.1 -> "21.1.", 1.21 -> "21.0.", 26.1 -> "26.1.".
func neoForgePrefix(mc string) string {
	if mc == "" {
		return ""
	}
	parts := strings.Split(mc, ".")
	if parts[0] == "1" {
		parts = parts[1:]
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	if len(parts) == 1 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:2], ".") + "."
}

func getRaw(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent)
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &httpx.StatusError{Code: resp.StatusCode, URL: u}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}
