package manifest

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

//go:embed manifest_data/*.json
var embeddedManifests embed.FS

// Source describes where a category was loaded from.
type Source struct {
	CategoryID string
	Origin     string // "embedded" | "override:<path>"
}

// LoadResult contains loaded categories and metadata about sources.
type LoadResult struct {
	Categories    []Category
	Sources       []Source
	EmbedCount    int
	OverridePath  string
	OverrideCount int
}

// LoadAll loads manifests from embedded FS first, then overrides from manifestDir if provided.
// File-system manifests override embedded ones with the same category ID.
func LoadAll(manifestDir string) ([]Category, error) {
	res, err := LoadAllWithSources(manifestDir)
	if err != nil {
		return nil, err
	}
	return res.Categories, nil
}

// LoadAllWithSources is like LoadAll but returns full source info for diagnostics.
func LoadAllWithSources(manifestDir string) (*LoadResult, error) {
	categories := make(map[string]Category)
	sources := make(map[string]string)

	// 1. Load embedded
	embedCount := 0
	entries, err := embeddedManifests.ReadDir("manifest_data")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, err := embeddedManifests.ReadFile("manifest_data/" + entry.Name())
			if err != nil {
				continue
			}
			var cat Category
			if err := json.Unmarshal(data, &cat); err != nil {
				continue
			}
			categories[cat.ID] = cat
			sources[cat.ID] = "embedded"
			embedCount++
		}
	}

	// 2. Override with file system manifests
	overrideCount := 0
	if manifestDir != "" {
		if info, err := os.Stat(manifestDir); err == nil && info.IsDir() {
			files, err := os.ReadDir(manifestDir)
			if err == nil {
				for _, f := range files {
					if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
						continue
					}
					fullPath := filepath.Join(manifestDir, f.Name())
					data, err := os.ReadFile(fullPath)
					if err != nil {
						continue
					}
					var cat Category
					if err := json.Unmarshal(data, &cat); err != nil {
						continue
					}
					categories[cat.ID] = cat
					sources[cat.ID] = "override:" + fullPath
					overrideCount++
				}
			}
		}
	}

	if len(categories) == 0 {
		return nil, fmt.Errorf("no manifests found (checked embedded and %q)", manifestDir)
	}

	result := make([]Category, 0, len(categories))
	srcList := make([]Source, 0, len(sources))
	for id, cat := range categories {
		result = append(result, cat)
		srcList = append(srcList, Source{CategoryID: id, Origin: sources[id]})
	}

	sort.Slice(result, func(i, j int) bool {
		return categoryOrder(result[i].ID) < categoryOrder(result[j].ID)
	})
	sort.Slice(srcList, func(i, j int) bool {
		return categoryOrder(srcList[i].CategoryID) < categoryOrder(srcList[j].CategoryID)
	})

	return &LoadResult{
		Categories:    result,
		Sources:       srcList,
		EmbedCount:    embedCount,
		OverridePath:  manifestDir,
		OverrideCount: overrideCount,
	}, nil
}

// FilterByPlatform keeps only packages installable on the given OS.
func FilterByPlatform(categories []Category, osName string) []Category {
	var filtered []Category
	for _, cat := range categories {
		newCat := Category{
			ID:          cat.ID,
			Name:        cat.Name,
			Description: cat.Description,
		}
		for _, sub := range cat.Subcategories {
			newSub := Subcategory{ID: sub.ID, Name: sub.Name}
			for _, pkg := range sub.Packages {
				if _, ok := pkg.Platforms[osName]; ok {
					newSub.Packages = append(newSub.Packages, pkg)
				}
			}
			if len(newSub.Packages) > 0 {
				newCat.Subcategories = append(newCat.Subcategories, newSub)
			}
		}
		if len(newCat.Subcategories) > 0 {
			filtered = append(filtered, newCat)
		}
	}
	return filtered
}

// FindPackage searches for a package by its full key (category.subcategory.package).
func FindPackage(categories []Category, key string) (cat *Category, sub *Subcategory, pkg *Package, ok bool) {
	for ci := range categories {
		c := &categories[ci]
		for si := range c.Subcategories {
			s := &c.Subcategories[si]
			for pi := range s.Packages {
				p := &s.Packages[pi]
				if c.ID+"."+s.ID+"."+p.ID == key {
					return c, s, p, true
				}
			}
		}
	}
	return nil, nil, nil, false
}

func categoryOrder(id string) int {
	order := map[string]int{
		"base_utils":        1,
		"terminals":         2,
		"languages":         3,
		"editors_sublime":   4,
		"editors_vscode":    5,
		"editors_jetbrains": 6,
		"editors_fresh":     7,
		"editors_other":     8,
		"dev_tools":         9,
		"containers":        10,
		"pentest":           11,
		"reversing":         12,
		"extras":            13,
	}
	if o, ok := order[id]; ok {
		return o
	}
	return 99
}
