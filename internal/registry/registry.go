package registry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/config"
)

var (
	reservedSlugs = map[string]bool{
		"con": true, "prn": true, "aux": true, "nul": true,
		"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
		"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
	}
	slugRegex = regexp.MustCompile(`[^a-z0-9\-]+`)
	multiDash = regexp.MustCompile(`-+`)
)

// SlugifyID normalizes a text into a kebab-case identifier, avoiding Windows reserved names.
func SlugifyID(text string) string {
	slug := strings.TrimSpace(strings.ToLower(text))
	slug = strings.ReplaceAll(slug, "_", "-")
	slug = slugRegex.ReplaceAllString(slug, "-")
	slug = multiDash.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "harness"
	}
	if reservedSlugs[slug] {
		return "harness-" + slug
	}
	return slug
}

// HarnessRecord holds metadata for a registered domain harness.
type HarnessRecord struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Domain         string  `json:"domain"`
	RegisteredAt   string  `json:"registered_at"`
	LastSwitchedAt *string `json:"last_switched_at"`
}

// HarnessListEntry augments HarnessRecord with active indicator and live status.
type HarnessListEntry struct {
	HarnessRecord
	IsActive bool           `json:"is_active"`
	Live     LiveInspection `json:"live"`
}

type registryData struct {
	Schema        string                   `json:"$schema,omitempty"`
	Version       string                   `json:"version"`
	ActiveHarness *string                  `json:"active_harness"`
	Harnesses     map[string]HarnessRecord `json:"harnesses"`
}

// HarnessRegistry manages the local domain harness catalog (~/.harness/config.json).
type HarnessRegistry struct {
	path string
	mu   sync.RWMutex
}

var (
	instance *HarnessRegistry
	instMu   sync.Mutex
)

// GetRegistry returns the singleton registry instance.
func GetRegistry() *HarnessRegistry {
	instMu.Lock()
	defer instMu.Unlock()
	cfgPath := config.GetConfigPath()
	if instance == nil || instance.path != cfgPath {
		instance = &HarnessRegistry{path: cfgPath}
	}
	return instance
}

// NewHarnessRegistry creates a new registry instance with custom path (used in tests).
func NewHarnessRegistry(customPath string) *HarnessRegistry {
	return &HarnessRegistry{path: customPath}
}

func defaultData() registryData {
	return registryData{
		Schema:        "https://json-schema.org/draft/2020-12/schema",
		Version:       "1.0.0",
		ActiveHarness: nil,
		Harnesses:     make(map[string]HarnessRecord),
	}
}

func (r *HarnessRegistry) load() registryData {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, err := os.Stat(r.path); os.IsNotExist(err) {
		return defaultData()
	}

	data, err := os.ReadFile(r.path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return defaultData()
	}

	var rd registryData
	if err := json.Unmarshal(data, &rd); err != nil {
		// Corrupted registry backup
		ts := time.Now().UTC().Format("20060102_150405")
		backupPath := fmt.Sprintf("%s.corrupted.%s", r.path, ts)
		_ = os.Rename(r.path, backupPath)
		fresh := defaultData()
		_ = r.save(fresh)
		return fresh
	}

	if rd.Harnesses == nil {
		rd.Harnesses = make(map[string]HarnessRecord)
	}
	return rd
}

func (r *HarnessRegistry) save(data registryData) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	config.EnforcePrivateDirPermissions(dir)

	randBytes := make([]byte, 4)
	_, _ = rand.Read(randBytes)
	tmpPath := fmt.Sprintf("%s.tmp.%d.%s", r.path, os.Getpid(), hex.EncodeToString(randBytes))

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(tmpPath, payload, 0600); err != nil {
		return err
	}
	config.EnforcePrivatePermissions(tmpPath)

	// Windows retry loop for file lock contention
	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = os.Rename(tmpPath, r.path)
		if err == nil {
			break
		}
		if attempt == maxAttempts-1 {
			_ = os.Remove(tmpPath)
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}

	config.EnforcePrivatePermissions(r.path)
	return nil
}

// Register adds or updates a domain harness in the catalog.
func (r *HarnessRegistry) Register(repoPath, name, domain string, makeActive, force bool) (*HarnessRecord, error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("cannot register harness: directory '%s' does not exist", absPath)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("cannot register harness: '%s' is not a directory", absPath)
	}

	// Validate harness markers
	isGit := false
	if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
		isGit = true
	}
	hasAreas := false
	if _, err := os.Stat(filepath.Join(absPath, "routing", "areas.yaml")); err == nil {
		hasAreas = true
	}
	hasConfig := false
	if _, err := os.Stat(filepath.Join(absPath, "config", "harness.config.json")); err == nil {
		hasConfig = true
	}
	hasAgents := false
	if _, err := os.Stat(filepath.Join(absPath, "AGENTS.md")); err == nil {
		hasAgents = true
	}

	if !force && !(isGit || hasAreas || hasConfig || hasAgents) {
		return nil, fmt.Errorf("directory '%s' does not appear to be a git repository or domain harness (missing .git, routing/areas.yaml, or AGENTS.md). Pass force=true to register anyway", absPath)
	}

	rd := r.load()

	// Check if this exact path is already registered under an existing ID
	var existingID string
	comparePath := absPath
	if runtime.GOOS == "windows" {
		comparePath = strings.ToLower(comparePath)
	}
	for id, rec := range rd.Harnesses {
		p := rec.Path
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		if p == comparePath {
			existingID = id
			break
		}
	}

	hid := existingID
	if hid == "" {
		friendlyName := name
		if friendlyName == "" {
			friendlyName = filepath.Base(absPath)
		}
		hid = SlugifyID(friendlyName)

		// Avoid ID collisions
		if _, exists := rd.Harnesses[hid]; exists {
			counter := 2
			for {
				candidate := fmt.Sprintf("%s-%d", hid, counter)
				if _, exists := rd.Harnesses[candidate]; !exists {
					hid = candidate
					break
				}
				counter++
			}
		}
	}

	nowISO := time.Now().UTC().Format(time.RFC3339)
	detectedDomain := domain
	if detectedDomain == "" {
		detectedDomain = InferDomain(absPath)
	}
	friendlyName := name
	if friendlyName == "" {
		if old, ok := rd.Harnesses[hid]; ok && old.Name != "" {
			friendlyName = old.Name
		} else {
			friendlyName = filepath.Base(absPath)
		}
	}

	registeredAt := nowISO
	var lastSwitchedAt *string
	if old, ok := rd.Harnesses[hid]; ok {
		registeredAt = old.RegisteredAt
		lastSwitchedAt = old.LastSwitchedAt
	}

	record := HarnessRecord{
		ID:             hid,
		Name:           friendlyName,
		Path:           absPath,
		Domain:         detectedDomain,
		RegisteredAt:   registeredAt,
		LastSwitchedAt: lastSwitchedAt,
	}

	rd.Harnesses[hid] = record

	if makeActive || rd.ActiveHarness == nil {
		rd.ActiveHarness = &hid
		record.LastSwitchedAt = &nowISO
		rd.Harnesses[hid] = record
	}

	if err := r.save(rd); err != nil {
		return nil, err
	}
	return &record, nil
}

// Deregister removes a harness by ID or path.
func (r *HarnessRegistry) Deregister(idOrPath string) bool {
	rd := r.load()
	targetID := r.resolveID(idOrPath, rd.Harnesses)
	if targetID == "" {
		return false
	}

	delete(rd.Harnesses, targetID)

	if rd.ActiveHarness != nil && *rd.ActiveHarness == targetID {
		if len(rd.Harnesses) > 0 {
			var firstKey string
			for k := range rd.Harnesses {
				firstKey = k
				break
			}
			rd.ActiveHarness = &firstKey
		} else {
			rd.ActiveHarness = nil
		}
	}

	_ = r.save(rd)
	return true
}

// Switch sets the active harness to the specified ID or path.
func (r *HarnessRegistry) Switch(idOrPath string) (*HarnessRecord, error) {
	rd := r.load()
	targetID := r.resolveID(idOrPath, rd.Harnesses)
	if targetID == "" {
		return nil, fmt.Errorf("harness '%s' is not registered in the catalog", idOrPath)
	}

	record := rd.Harnesses[targetID]
	fi, err := os.Stat(record.Path)
	if err != nil {
		return nil, fmt.Errorf("registered path for harness '%s' does not exist on disk: %s", targetID, record.Path)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("registered path for harness '%s' is not a directory: %s", targetID, record.Path)
	}

	nowISO := time.Now().UTC().Format(time.RFC3339)
	rd.ActiveHarness = &targetID
	record.LastSwitchedAt = &nowISO
	rd.Harnesses[targetID] = record

	if err := r.save(rd); err != nil {
		return nil, err
	}
	return &record, nil
}

// GetHarness retrieves a stored harness record by ID or path.
func (r *HarnessRegistry) GetHarness(idOrPath string) (*HarnessRecord, bool) {
	rd := r.load()
	targetID := r.resolveID(idOrPath, rd.Harnesses)
	if targetID == "" {
		return nil, false
	}
	rec := rd.Harnesses[targetID]
	return &rec, true
}

// GetActiveHarness returns the active harness record, if any.
func (r *HarnessRegistry) GetActiveHarness() (*HarnessRecord, bool) {
	rd := r.load()
	if rd.ActiveHarness == nil {
		return nil, false
	}
	rec, ok := rd.Harnesses[*rd.ActiveHarness]
	if !ok {
		return nil, false
	}
	return &rec, true
}

// ListHarnesses returns all registered harnesses with live metadata.
func (r *HarnessRegistry) ListHarnesses() []HarnessListEntry {
	rd := r.load()
	activeID := ""
	if rd.ActiveHarness != nil {
		activeID = *rd.ActiveHarness
	}

	results := make([]HarnessListEntry, len(rd.Harnesses))
	var wg sync.WaitGroup
	var idxMu sync.Mutex
	idx := 0

	for hid, rec := range rd.Harnesses {
		wg.Add(1)
		go func(hID string, hRec HarnessRecord) {
			defer wg.Done()
			live := InspectHarness(hRec.Path)
			entry := HarnessListEntry{
				HarnessRecord: hRec,
				IsActive:      (hID == activeID),
				Live:          live,
			}
			idxMu.Lock()
			results[idx] = entry
			idx++
			idxMu.Unlock()
		}(hid, rec)
	}

	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].ID < results[j].ID
	})
	return results
}

func (r *HarnessRegistry) resolveID(idOrPath string, harnesses map[string]HarnessRecord) string {
	if _, ok := harnesses[idOrPath]; ok {
		return idOrPath
	}

	// Case-insensitive ID lookup
	lower := strings.ToLower(idOrPath)
	for id := range harnesses {
		if strings.ToLower(id) == lower {
			return id
		}
	}

	// Path lookup
	absPath, err := filepath.Abs(idOrPath)
	if err == nil {
		comparePath := absPath
		if runtime.GOOS == "windows" {
			comparePath = strings.ToLower(comparePath)
		}
		for id, rec := range harnesses {
			p, err := filepath.Abs(rec.Path)
			if err == nil {
				if runtime.GOOS == "windows" {
					p = strings.ToLower(p)
				}
				if p == comparePath {
					return id
				}
			}
		}
	}

	return ""
}
