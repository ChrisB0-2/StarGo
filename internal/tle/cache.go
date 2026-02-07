package tle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cache manages TLE data files on disk.
type Cache struct {
	dir      string
	maxFiles int
}

// NewCache creates a Cache that stores files in dir and keeps at most maxFiles.
func NewCache(dir string, maxFiles int) *Cache {
	if maxFiles <= 0 {
		maxFiles = 5
	}
	return &Cache{
		dir:      dir,
		maxFiles: maxFiles,
	}
}

// Write saves data to a timestamped file and prunes old files beyond maxFiles.
func (c *Cache) Write(data []byte, ts time.Time) error {
	if err := c.ensureDir(); err != nil {
		return err
	}

	filename := fmt.Sprintf("tle_%d.txt", ts.Unix())
	path := filepath.Join(c.dir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing cache file: %w", err)
	}

	return c.prune()
}

// LoadLatest reads the newest cache file by timestamp in the filename.
// Returns the data, the timestamp, and any error.
func (c *Cache) LoadLatest() ([]byte, time.Time, error) {
	files, err := c.listFiles()
	if err != nil {
		return nil, time.Time{}, err
	}

	if len(files) == 0 {
		return nil, time.Time{}, fmt.Errorf("no cache files found")
	}

	// Files are sorted oldest first; take the last one.
	latest := files[len(files)-1]
	data, err := os.ReadFile(filepath.Join(c.dir, latest.name))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("reading cache file: %w", err)
	}

	return data, latest.ts, nil
}

type cacheFile struct {
	name string
	ts   time.Time
}

func (c *Cache) listFiles() ([]cacheFile, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing cache dir: %w", err)
	}

	var files []cacheFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "tle_") || !strings.HasSuffix(name, ".txt") {
			continue
		}
		// Extract unix timestamp from filename.
		tsStr := strings.TrimPrefix(name, "tle_")
		tsStr = strings.TrimSuffix(tsStr, ".txt")
		unix, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			continue
		}
		files = append(files, cacheFile{name: name, ts: time.Unix(unix, 0)})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ts.Before(files[j].ts)
	})

	return files, nil
}

func (c *Cache) prune() error {
	files, err := c.listFiles()
	if err != nil {
		return err
	}

	if len(files) <= c.maxFiles {
		return nil
	}

	// Remove oldest files.
	toRemove := files[:len(files)-c.maxFiles]
	for _, f := range toRemove {
		path := filepath.Join(c.dir, f.name)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("pruning cache file %s: %w", f.name, err)
		}
	}

	return nil
}

func (c *Cache) ensureDir() error {
	return os.MkdirAll(c.dir, 0755)
}
