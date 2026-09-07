package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"
)

type pathMutation struct {
	Path          string
	Kind          string // directory, file, link, remove
	Source        string
	Data          []byte
	Target        string
	Mode          fs.FileMode
	Expected      string
	ContentDigest string
}

type operationStep struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Before string `json:"before"`
	After  string `json:"after"`
	State  string `json:"state"`
	Backup string `json:"backup"`
	Stage  string `json:"stage"`
}

type operationRecord struct {
	External      bool             `json:"external,omitzero"`
	Native        *nativeOperation `json:"native,omitempty"`
	SchemaVersion int              `json:"schemaVersion"`
	ID            string           `json:"id"`
	Command       string           `json:"command"`
	Created       time.Time        `json:"created"`
	State         string           `json:"state"`
	Steps         []operationStep  `json:"steps"`
	Affected      []string         `json:"affected,omitempty"`
	Error         string           `json:"error,omitempty"`
	directory     string
}

func stateFile(name string) (string, error) {
	dir, err := skillctlDirectory()
	return filepath.Join(dir, name), err
}

func saveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomically(path, append(data, '\n'), 0o600)
}

func (r *operationRecord) save() error {
	return saveJSON(filepath.Join(r.directory, "operation.json"), r)
}

// fingerprint uses exact bytes, modes and link destinations, without following
// links. Transaction preconditions must notice edits even when a provider uses
// a normalized hash (for example, CRLF normalization).
func fingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	h := sha256.New()
	add := func(path, rel string, info fs.FileInfo) error {
		fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(rel), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00", target)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported filesystem entry: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, readErr := io.Copy(h, file)
		closeErr := file.Close()
		_, _ = h.Write([]byte{0})
		return errors.Join(readErr, closeErr)
	}
	if info.IsDir() {
		err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(path, current)
			if err != nil {
				return err
			}
			return add(current, rel, info)
		})
	} else {
		err = add(path, ".", info)
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copySnapshot(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(link, target)
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copySnapshot(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(target, info.Mode().Perm())
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cannot snapshot special file: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

func mutation(path, kind string) (pathMutation, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return pathMutation{}, err
	}
	absolute = canonicalLocation(absolute)
	expected, err := fingerprint(absolute)
	return pathMutation{Path: absolute, Kind: kind, Expected: expected}, err
}

func prepareOperation(command string, changes []pathMutation, affected []string) (*operationRecord, error) {
	dir, err := stateFile("history")
	if err != nil {
		return nil, err
	}
	id := uuid.NewV7().String()
	r := &operationRecord{SchemaVersion: 1, ID: id, Command: command, Created: time.Now().UTC(), State: "preparing", Affected: affected, directory: canonicalLocation(filepath.Join(dir, id))}
	for i, change := range changes {
		if within(change.Path, r.directory) || within(r.directory, change.Path) {
			return nil, fmt.Errorf("operation journal must be outside its targets: %s", change.Path)
		}
		if change.Path == "" || !filepath.IsAbs(change.Path) || filepath.Dir(change.Path) == change.Path {
			return nil, fmt.Errorf("invalid operation target: %s", change.Path)
		}
		for _, earlier := range changes[:i] {
			if within(earlier.Path, change.Path) || within(change.Path, earlier.Path) {
				return nil, fmt.Errorf("overlapping operation paths: %s and %s", earlier.Path, change.Path)
			}
		}
	}
	if err := os.MkdirAll(r.directory, 0o700); err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(r.directory)
		}
	}()
	for i, change := range changes {
		before, err := fingerprint(change.Path)
		if err != nil {
			return nil, err
		}
		if change.Expected != "" && before != change.Expected {
			return nil, fmt.Errorf("operation precondition changed: %s", change.Path)
		}
		beforePath, afterPath := r.imagePath("before", i), r.imagePath("after", i)
		if err := os.MkdirAll(filepath.Dir(beforePath), 0o700); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(afterPath), 0o700); err != nil {
			return nil, err
		}
		if before != "missing" {
			if err := copySnapshot(change.Path, beforePath); err != nil {
				return nil, err
			}
			if hash, err := fingerprint(beforePath); err != nil || hash != before {
				return nil, fmt.Errorf("backup verification failed: %s", change.Path)
			}
		}
		switch change.Kind {
		case "remove":
		case "directory":
			if change.ContentDigest != "" {
				if err := copyDirectory(change.Source, afterPath); err != nil {
					return nil, err
				}
				if digest, err := hashDirectory(afterPath); err != nil || digest != change.ContentDigest {
					return nil, fmt.Errorf("source content changed while preparing %s", change.Path)
				}
			} else if err := copySnapshot(change.Source, afterPath); err != nil {
				return nil, err
			}
		case "file":
			mode := change.Mode
			if mode == 0 {
				mode = 0o600
			}
			if err := os.WriteFile(afterPath, change.Data, mode); err != nil {
				return nil, err
			}
		case "link":
			if err := os.Symlink(change.Target, afterPath); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported mutation: %s", change.Kind)
		}
		after, err := fingerprint(afterPath)
		if err != nil {
			return nil, err
		}
		parent := filepath.Dir(change.Path)
		r.Steps = append(r.Steps, operationStep{Path: change.Path, Action: change.Kind, Before: before, After: after, State: "prepared", Backup: filepath.Join(parent, ".skillctl-backup-"+id+"-"+strconv.Itoa(i)), Stage: filepath.Join(parent, ".skillctl-stage-"+id+"-"+strconv.Itoa(i))})
	}
	r.State = "prepared"
	if err := r.save(); err != nil {
		return nil, err
	}
	complete = true
	return r, nil
}

func (r *operationRecord) imagePath(phase string, index int) string {
	return filepath.Join(r.directory, phase, strconv.Itoa(index))
}

func (r *operationRecord) apply() error {
	r.State = "applying"
	if err := r.save(); err != nil {
		return err
	}
	for i := range r.Steps {
		step := &r.Steps[i]
		if err := r.applyStep(i); err != nil {
			r.Error = err.Error()
			if restoreErr := r.restore(); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("recovery required for operation %s: %w", r.ID, restoreErr))
			}
			return fmt.Errorf("operation %s rolled back: %w", r.ID, err)
		}
		step.State = "applied"
		if err := r.save(); err != nil {
			return errors.Join(err, r.restore())
		}
	}
	r.State = "committed"
	if err := r.save(); err != nil {
		return errors.Join(err, r.restore())
	}
	for _, step := range r.Steps {
		_ = os.RemoveAll(step.Backup)
		_ = os.RemoveAll(step.Stage)
	}
	return nil
}

func (r *operationRecord) applyStep(i int) error {
	step := &r.Steps[i]
	current, err := fingerprint(step.Path)
	if err != nil {
		return err
	}
	if current != step.Before {
		return fmt.Errorf("local state changed before execution: %s", step.Path)
	}
	if err := os.MkdirAll(filepath.Dir(step.Path), 0o755); err != nil {
		return err
	}
	if step.After != "missing" {
		if err := copySnapshot(r.imagePath("after", i), step.Stage); err != nil {
			return err
		}
		if hash, err := fingerprint(step.Stage); err != nil || hash != step.After {
			return fmt.Errorf("staged content verification failed: %s", step.Path)
		}
	}
	// Revalidate after preparing a potentially large directory.
	if hash, err := fingerprint(step.Path); err != nil || hash != step.Before {
		return fmt.Errorf("local state changed while staging: %s", step.Path)
	}
	step.State = "applying"
	if err := r.save(); err != nil {
		return err
	}
	if step.Before != "missing" {
		if err := os.Rename(step.Path, step.Backup); err != nil {
			return err
		}
	}
	if step.After != "missing" {
		if err := os.Rename(step.Stage, step.Path); err != nil {
			return err
		}
	}
	if hash, err := fingerprint(step.Path); err != nil || hash != step.After {
		return fmt.Errorf("post-operation verification failed: %s", step.Path)
	}
	return nil
}

func (r *operationRecord) restore() error {
	var failures []error
	for i := len(r.Steps) - 1; i >= 0; i-- {
		step := &r.Steps[i]
		if step.State == "prepared" || step.State == "restored" {
			_ = os.RemoveAll(step.Stage)
			continue
		}
		current, err := fingerprint(step.Path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current == step.Before {
			step.State = "restored"
			_ = os.RemoveAll(step.Backup)
			_ = os.RemoveAll(step.Stage)
			continue
		}
		backup, err := fingerprint(step.Backup)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current != step.After && !(current == "missing" && backup == step.Before) {
			failures = append(failures, fmt.Errorf("new local changes prevent recovery: %s", step.Path))
			continue
		}
		if step.Before != "missing" {
			if hash, err := fingerprint(r.imagePath("before", i)); err != nil || hash != step.Before {
				failures = append(failures, fmt.Errorf("backup is invalid: %s", step.Path))
				continue
			}
		}
		if err := os.RemoveAll(step.Path); err != nil {
			failures = append(failures, err)
			continue
		}
		if step.Before != "missing" {
			if err := copySnapshot(r.imagePath("before", i), step.Path); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if hash, err := fingerprint(step.Path); err != nil || hash != step.Before {
			failures = append(failures, fmt.Errorf("restored content verification failed: %s", step.Path))
			continue
		}
		step.State = "restored"
		_ = os.RemoveAll(step.Backup)
		_ = os.RemoveAll(step.Stage)
	}
	r.State = "rolled_back"
	if len(failures) > 0 {
		r.State = "recovery_required"
	}
	return errors.Join(append(failures, r.save())...)
}

func readOperations() ([]operationRecord, error) {
	root, err := stateFile("history")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []operationRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var result []operationRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := uuid.Parse(entry.Name()); err != nil {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(filepath.Join(directory, "operation.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // interrupted preparation cannot modify targets
		if err != nil {
			return nil, err
		}
		var r operationRecord
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("invalid operation %s: %w", entry.Name(), err)
		}
		if r.SchemaVersion != 1 || r.ID != entry.Name() {
			return nil, fmt.Errorf("unsupported operation record: %s", entry.Name())
		}
		r.directory = directory
		result = append(result, r)
	}
	slices.SortFunc(result, func(a, b operationRecord) int { return b.Created.Compare(a.Created) })
	return result, nil
}

func recoverOperations(except ...string) error {
	operations, err := readOperations()
	if err != nil {
		return err
	}
	for _, r := range operations {
		if slices.Contains(except, r.ID) {
			continue
		}
		if r.External && (r.State == "external_applying" || r.State == "recovery_required") {
			if err := recoverExternal(&r); err != nil {
				return err
			}
			continue
		}
		if r.Native != nil {
			if r.State == "native_applying" || r.State == "recovery_required" {
				if err := recoverNative(&r); err != nil {
					return fmt.Errorf("operation %s: %w", r.ID, err)
				}
			}
			continue
		}
		if r.State == "applying" || r.State == "recovery_required" || r.State == "prepared" {
			if err := r.restore(); err != nil {
				return fmt.Errorf("operation %s requires recovery: %w", r.ID, err)
			}
		}
	}
	return nil
}

func rollbackMutations(id string) ([]pathMutation, error) {
	operations, err := readOperations()
	if err != nil {
		return nil, err
	}
	for _, r := range operations {
		if r.ID != id && id != "latest" {
			continue
		}
		if r.State != "committed" && !(r.External && r.State == "recovery_required" && id != "latest") {
			if id == "latest" {
				continue
			}
			return nil, fmt.Errorf("operation %s is %s, not committed", r.ID, r.State)
		}
		var changes []pathMutation
		for i := len(r.Steps) - 1; i >= 0; i-- {
			step := r.Steps[i]
			change, err := mutation(step.Path, "remove")
			if err != nil {
				return nil, err
			}
			if change.Expected != step.After {
				return nil, fmt.Errorf("changes since operation %s prevent rollback: %s", r.ID, step.Path)
			}
			if step.Before != "missing" {
				source := r.imagePath("before", i)
				if hash, err := fingerprint(source); err != nil || hash != step.Before {
					return nil, fmt.Errorf("rollback backup is invalid: %s", step.Path)
				}
				info, err := os.Lstat(source)
				if err != nil {
					return nil, err
				}
				switch {
				case info.Mode()&os.ModeSymlink != 0:
					change.Kind = "link"
					change.Target, err = os.Readlink(source)
				case info.IsDir():
					change.Kind, change.Source = "directory", source
				default:
					change.Kind, change.Mode = "file", info.Mode().Perm()
					change.Data, err = os.ReadFile(source)
				}
				if err != nil {
					return nil, err
				}
			}
			changes = append(changes, change)
		}
		return changes, nil
	}
	return nil, fmt.Errorf("operation not found: %s", strings.TrimSpace(id))
}
