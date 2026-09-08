package installhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

const historyParserVersion = 3

type CacheStats struct {
	FilesScanned int   `json:"filesScanned"`
	FilesReused  int   `json:"filesReused"`
	BytesRead    int64 `json:"bytesRead"`
}

type cachedHistoryFile struct {
	Stamp      string
	Candidates map[string][]Candidate
}

type historyCache struct {
	Version int
	Files   map[string]cachedHistoryFile
}

// ReadRootsCached incrementally reparses changed files. Cache hits require
// inode, size, modification time AND change time; append, replacement, restored
// mtime and truncation invalidate the complete file. We do not assume logs are
// append-only. Platforms without a change-time identity fall back to parsing.
// The index holds candidate metadata only, never conversation bodies.
func ReadRootsCached(ctx context.Context, roots []string, cachePath string) (map[string][]Candidate, CacheStats, error) {
	var prior historyCache
	if info, err := os.Stat(cachePath); err == nil && info.Size() <= 8<<20 {
		if data, err := os.ReadFile(cachePath); err == nil {
			if err := json.Unmarshal(data, &prior); err != nil {
				prior = historyCache{}
			}
		}
	}
	if prior.Version != historyParserVersion {
		prior.Files = nil
	}
	next := historyCache{Version: historyParserVersion, Files: map[string]cachedHistoryFile{}}
	result := map[string][]Candidate{}
	stats := CacheStats{}
	seen := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" || seen[path] {
				return nil
			}
			seen[path] = true
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			stamp := historyFileStamp(info)
			cached, found := prior.Files[path]
			if !found || stamp == "" || cached.Stamp != stamp {
				cached = cachedHistoryFile{Stamp: stamp, Candidates: map[string][]Candidate{}}
				if err := scanHistoryFileContext(ctx, path, cached.Candidates, map[string]bool{}); err != nil {
					return err
				}
				stats.FilesScanned++
				stats.BytesRead += info.Size()
				after, err := os.Stat(path)
				if err != nil || stamp != "" && historyFileStamp(after) != stamp || stamp == "" && (after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime())) {
					return fmt.Errorf("install history changed during inspection")
				}
			} else {
				stats.FilesReused++
			}
			safe := true
			for name, candidates := range cached.Candidates {
				result[name] = append(result[name], candidates...)
				for _, candidate := range candidates {
					safe = safe && cacheSafeSource(candidate.Source)
				}
			}
			if safe && stamp != "" {
				next.Files[path] = cached
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, stats, err
		}
	}
	for name := range result {
		slices.SortStableFunc(result[name], func(a, b Candidate) int { return b.When.Compare(a.When) })
	}
	if ctx.Err() == nil {
		saveHistoryCache(cachePath, next)
	}
	return result, stats, nil
}

func cacheSafeSource(source string) bool {
	value, err := url.Parse(source)
	if err != nil {
		return false
	}
	if value.User != nil {
		return false
	}
	for key := range value.Query() {
		key = strings.ToLower(key)
		if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "signature") || strings.Contains(key, "credential") || key == "key" || key == "auth" {
			return false
		}
	}
	return true
}

func saveHistoryCache(path string, cache historyCache) {
	data, err := json.Marshal(cache)
	if err != nil || len(data) > 8<<20 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".history-index-*")
	if err != nil {
		return
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(temporary, path)
	}
}

// Reflect only documented Stat_t field names, avoiding OS-specific casts in
// this portable parser. Missing fields disable reuse rather than weaken it.
func historyFileStamp(info os.FileInfo) string {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return ""
	}
	value = value.Elem()
	if value.Kind() != reflect.Struct {
		return ""
	}
	device, inode := value.FieldByName("Dev"), value.FieldByName("Ino")
	change := value.FieldByName("Ctim")
	if !change.IsValid() {
		change = value.FieldByName("Ctimespec")
	}
	if !device.IsValid() || !inode.IsValid() || !change.IsValid() || change.Kind() != reflect.Struct {
		return ""
	}
	seconds, nanos := change.FieldByName("Sec"), change.FieldByName("Nsec")
	if !seconds.IsValid() || !nanos.IsValid() {
		return ""
	}
	return fmt.Sprintf("%v:%v:%d:%d:%v:%v", device.Interface(), inode.Interface(), info.Size(), info.ModTime().UnixNano(), seconds.Interface(), nanos.Interface())
}
