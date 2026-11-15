package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var validCategories = map[string]bool{
	"general": true, "coding": true, "reasoning": true, "writing": true,
	"vision": true, "audio": true, "embedding": true, "uncategorized": true,
}

type ModelMetadataRequest struct {
	Path       string   `json:"path"`
	Categories []string `json:"categories"`
	License    string   `json:"license,omitempty"`
	SourceURL  string   `json:"sourceUrl,omitempty"`
	CatalogID  string   `json:"catalogId,omitempty"`
	MMProjPath string   `json:"mmprojPath,omitempty"`
	Notes      string   `json:"notes,omitempty"`
}

func (a *App) loadRegistry() error {
	data, err := os.ReadFile(a.registryPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []Model
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("model registry is invalid (%s): %w; preserve the file and correct its JSON before continuing", a.registryPath, err)
	}
	for _, m := range items {
		if m.Path != "" {
			a.registry[filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(m.Path, "\\", "/"))))] = m
		}
	}
	return nil
}

func (a *App) saveRegistryLocked() error {
	items := make([]Model, 0, len(a.registry))
	for _, m := range a.registry {
		items = append(items, m)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Path) < strings.ToLower(items[j].Path) })
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.registryPath), 0755); err != nil {
		return err
	}
	tmp := a.registryPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.registryPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (a *App) scanModels() ([]Model, error) {
	base := filepath.Join(a.root, "Models")
	items := []Model{}
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".gguf") {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		role := "model"
		if strings.HasPrefix(strings.ToLower(d.Name()), "mmproj") || strings.Contains(strings.ToLower(d.Name()), "mmproj-") {
			role = "projector"
		}
		gguf := readGGUFInfo(path)
		m := Model{Path: rel, Name: d.Name(), SizeBytes: st.Size(), Modified: st.ModTime().Format(time.RFC3339), Role: role, Categories: inferCategories(rel), LicenseVerified: false, Architecture: gguf.Architecture, ArchitectureRecognized: gguf.Recognized, MetadataReadable: gguf.MetadataReadable, Quantization: gguf.Quantization}
		a.registryMu.Lock()
		saved, ok := a.registry[rel]
		a.registryMu.Unlock()
		if ok {
			m.Categories = saved.Categories
			m.License = saved.License
			m.LicenseVerified = saved.LicenseVerified
			m.SourceURL = saved.SourceURL
			m.CatalogID = saved.CatalogID
			m.MMProjPath = saved.MMProjPath
			m.Notes = saved.Notes
		}
		if role == "projector" {
			m.Categories = []string{}
		}
		items = append(items, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Path) < strings.ToLower(items[j].Path) })
	return items, nil
}

func inferCategories(path string) []string {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	out := []string{}
	add := func(c string) {
		for _, existing := range out {
			if existing == c {
				return
			}
		}
		out = append(out, c)
	}
	parts := strings.Split(p, "/")
	for _, part := range parts[:maxInt(0, len(parts)-1)] {
		if validCategories[part] && part != "uncategorized" {
			add(part)
		}
	}
	if strings.Contains(p, "coder") || strings.Contains(p, "coding") || strings.Contains(p, "starcoder") {
		add("coding")
	}
	if strings.Contains(p, "think") || strings.Contains(p, "reason") || strings.Contains(p, "deepseek-r1") {
		add("reasoning")
	}
	if strings.Contains(p, "write") || strings.Contains(p, "creative") {
		add("writing")
	}
	if strings.Contains(p, "vision") || strings.Contains(p, "-vl") || strings.Contains(p, "_vl") || strings.Contains(p, "qwen3vl") {
		add("vision")
	}
	if strings.Contains(p, "embed") {
		add("embedding")
	}
	if strings.Contains(p, "audio") || strings.Contains(p, "whisper") {
		add("audio")
	}
	if len(out) == 0 {
		add("general")
	}
	sort.Strings(out)
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (a *App) refreshModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	items, err := a.scanModels()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	a.registryMu.Lock()
	added := 0
	for _, m := range items {
		if old, ok := a.registry[m.Path]; ok {
			// Keep the user's metadata and tags, but update the observed file facts.
			m.Categories = old.Categories
			m.License = old.License
			m.LicenseVerified = old.LicenseVerified
			m.SourceURL = old.SourceURL
			m.CatalogID = old.CatalogID
			m.MMProjPath = old.MMProjPath
			m.Notes = old.Notes
		} else {
			added++
		}
		if m.Role == "projector" {
			m.Categories = []string{}
		}
		a.registry[m.Path] = m
	}
	err = a.saveRegistryLocked()
	a.registryMu.Unlock()
	if err != nil {
		writeError(w, 500, "could not save registry: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": items, "count": len(items), "newFiles": added, "message": "Detection complete. No downloads were performed."})
}

func (a *App) updateModelMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var req ModelMetadataRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, 400, "invalid JSON request: "+err.Error())
		return
	}
	path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(req.Path), "\\", "/"))))
	if path == "." || path == ".." || strings.HasPrefix(path, "../") || filepath.IsAbs(path) {
		writeError(w, 400, "path must be relative to Models")
		return
	}
	full, err := a.safeModelPath(path)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() || !strings.EqualFold(filepath.Ext(full), ".gguf") {
		writeError(w, 404, "model file not found")
		return
	}
	for _, category := range req.Categories {
		if !isCategorySlug(category) {
			writeError(w, 400, "category must use 1–32 letters, numbers, and hyphens: "+category)
			return
		}
	}
	categories := []string{}
	for _, c := range req.Categories {
		c = strings.ToLower(c)
		found := false
		for _, old := range categories {
			if old == c {
				found = true
				break
			}
		}
		if !found {
			categories = append(categories, c)
		}
	}
	sort.Strings(categories)
	mmproj := ""
	if req.MMProjPath != "" {
		p, e := a.safeModelPath(req.MMProjPath)
		if e != nil {
			writeError(w, 400, "projector: "+e.Error())
			return
		}
		if info, e := os.Stat(p); e != nil || info.IsDir() || !strings.EqualFold(filepath.Ext(p), ".gguf") {
			writeError(w, 400, "projector file not found")
			return
		}
		mmproj = filepath.ToSlash(req.MMProjPath)
	}
	a.registryMu.Lock()
	old, exists := a.registry[path]
	if !exists {
		old = Model{Path: path, Name: filepath.Base(path), Role: "model", LicenseVerified: false}
	}
	old.Categories = categories
	old.License = strings.TrimSpace(req.License)
	old.SourceURL = strings.TrimSpace(req.SourceURL)
	old.CatalogID = strings.TrimSpace(req.CatalogID)
	if old.CatalogID != "" {
		entries, catalogErr := readModelCatalog(a.root)
		if catalogErr != nil {
			a.registryMu.Unlock()
			writeError(w, 500, "cannot read model catalog: "+catalogErr.Error())
			return
		}
		var entry CatalogEntry
		ok := false
		for _, candidate := range entries {
			if candidate.ID == old.CatalogID && candidate.LicenseVerified {
				entry = candidate
				ok = true
				break
			}
		}
		if !ok {
			a.registryMu.Unlock()
			writeError(w, 400, "unknown catalogId; only curated catalog entries can verify an upstream license")
			return
		}
		old.License = entry.License
		old.SourceURL = entry.SourceURL
		old.LicenseVerified = true
	} else {
		old.LicenseVerified = false
	}
	old.MMProjPath = mmproj
	old.Notes = strings.TrimSpace(req.Notes)
	a.registry[path] = old
	err = a.saveRegistryLocked()
	a.registryMu.Unlock()
	if err != nil {
		writeError(w, 500, "could not save registry: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true, "model": old, "licenseWarning": "User-entered license text is not independently verified."})
}

type CatalogFile struct {
	FileName    string `json:"fileName"`
	Role        string `json:"role"`
	Required    bool   `json:"required"`
	ApproxBytes int64  `json:"approxBytes,omitempty"`
}
type CatalogEntry struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Repository      string        `json:"repository"`
	SourceURL       string        `json:"sourceUrl"`
	License         string        `json:"license"`
	LicenseVerified bool          `json:"licenseVerified"`
	Distribution    string        `json:"distribution,omitempty"`
	Categories      []string      `json:"categories"`
	Description     string        `json:"description"`
	Files           []CatalogFile `json:"files"`
	ManualSteps     []string      `json:"manualSteps"`
	Notes           []string      `json:"notes"`
}

func readModelCatalog(root string) ([]CatalogEntry, error) {
	path := filepath.Join(root, "Config", "model-catalog.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var items []CatalogEntry
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" {
			return nil, fmt.Errorf("every entry requires id and name")
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("duplicate catalog id %q", item.ID)
		}
		seen[item.ID] = true
	}
	return items, nil
}

func (a *App) catalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	items, err := readModelCatalog(a.root)
	if err != nil {
		writeError(w, 500, "cannot read model catalog: "+err.Error())
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, 200, map[string]any{"models": items, "count": len(items), "policy": "Catalog metadata is read from Config/model-catalog.json. All downloads are manual; the application never downloads models."})
}

func isCategorySlug(category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	if len(category) < 1 || len(category) > 32 {
		return false
	}
	first := category[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for _, r := range category {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func isKnownCategory(category string) bool { return validCategories[strings.ToLower(category)] }
