// Package catalog is the built-in, read-only filter category catalog (catalog.yaml, embedded) and
// its sync into catalog-managed filter lists. Operators toggle categories and sources; they cannot add,
// edit or remove sources: catalog changes ship with Nexora releases.
package catalog

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed catalog.yaml
var raw []byte

// Catalog is the parsed catalog.yaml.
type Catalog struct {
	Version    int        `yaml:"version"`
	Categories []Category `yaml:"categories"`
}

// Category groups curated sources under one key operators enable.
type Category struct {
	Key         string   `yaml:"key"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Sources     []Source `yaml:"sources"`
}

// Source is one curated list with its license metadata.
type Source struct {
	Key                    string `yaml:"key"`
	Name                   string `yaml:"name"`
	URL                    string `yaml:"url"`
	Format                 string `yaml:"format"`
	ArchiveMember          string `yaml:"archive_member"`
	License                string `yaml:"license"`
	LicenseURL             string `yaml:"license_url"`
	Attribution            string `yaml:"attribution"`
	CommercialUse          bool   `yaml:"commercial_use"`
	Notice                 string `yaml:"notice"`
	DefaultEnabled         bool   `yaml:"default_enabled"`
	RefreshIntervalSeconds int    `yaml:"refresh_interval_seconds"`
}

var (
	categoryKeyRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	sourceKeyRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
)

// Load returns the embedded catalog, validated.
func Load() (*Catalog, error) { return Parse(raw) }

// Raw returns the embedded catalog bytes.
func Raw() []byte { return raw }

// Digest is the sha256 hex of data; Sync records it so an unchanged catalog writes nothing.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Parse decodes and validates a catalog, returning the first violation.
func Parse(data []byte) (*Catalog, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Catalog) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("catalog version %d, want 1", c.Version)
	}
	categories := map[string]bool{}
	sources := map[string]bool{}
	for _, cat := range c.Categories {
		if !categoryKeyRe.MatchString(cat.Key) {
			return fmt.Errorf("category key %s must match ^[a-z][a-z0-9-]{0,31}$", cat.Key)
		}
		if categories[cat.Key] {
			return fmt.Errorf("duplicate category key %s", cat.Key)
		}
		categories[cat.Key] = true
		if cat.Name == "" || cat.Description == "" || len(cat.Sources) == 0 {
			return fmt.Errorf("category %s: name, description and at least one source are required", cat.Key)
		}
		for _, s := range cat.Sources {
			if err := validateSource(cat.Key, s, sources); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSource(category string, s Source, seen map[string]bool) error {
	switch {
	case !sourceKeyRe.MatchString(s.Key):
		return fmt.Errorf("source key %s must match ^[a-z0-9][a-z0-9-]{0,47}$", s.Key)
	case seen[s.Key]:
		// Unique across the catalog: the mirror path is the source key.
		return fmt.Errorf("duplicate source key %s", s.Key)
	case !strings.HasPrefix(s.URL, "https://"):
		return fmt.Errorf("source %s: url must be https", s.Key)
	case s.Format != "domains" && s.Format != "hosts" && s.Format != "wildcard":
		return fmt.Errorf("source %s: format %s must be domains, hosts or wildcard", s.Key, s.Format)
	case s.ArchiveMember != "" && !strings.HasSuffix(s.URL, ".tar.gz"):
		return fmt.Errorf("source %s: archive_member needs a .tar.gz url", s.Key)
	case s.License == "" || s.LicenseURL == "" || s.Attribution == "":
		return fmt.Errorf("source %s: license, license_url and attribution are required", s.Key)
	case !s.CommercialUse && s.Notice == "":
		return fmt.Errorf("source %s: commercial_use false needs a notice", s.Key)
	case s.RefreshIntervalSeconds < 3600:
		return fmt.Errorf("source %s: refresh_interval_seconds must be at least 3600", s.Key)
	case len(ListName(category, s.Key)) > 64:
		return fmt.Errorf("source %s: list name %s exceeds 64 characters", s.Key, ListName(category, s.Key))
	}
	seen[s.Key] = true
	return nil
}

// Category returns the category with key.
func (c *Catalog) Category(key string) (Category, bool) {
	for _, cat := range c.Categories {
		if cat.Key == key {
			return cat, true
		}
	}
	return Category{}, false
}

// Source returns the source sourceKey of category categoryKey.
func (c *Catalog) Source(categoryKey, sourceKey string) (Source, bool) {
	cat, ok := c.Category(categoryKey)
	if !ok {
		return Source{}, false
	}
	for _, s := range cat.Sources {
		if s.Key == sourceKey {
			return s, true
		}
	}
	return Source{}, false
}

// Position is the 1-based order of the source across the whole catalog, 0 when unknown.
func (c *Catalog) Position(categoryKey, sourceKey string) int {
	n := 0
	for _, cat := range c.Categories {
		for _, s := range cat.Sources {
			n++
			if cat.Key == categoryKey && s.Key == sourceKey {
				return n
			}
		}
	}
	return 0
}

// ListName is the filter_lists name of a catalog source.
func ListName(categoryKey, sourceKey string) string {
	return "catalog:" + categoryKey + ":" + sourceKey
}
