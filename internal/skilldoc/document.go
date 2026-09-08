package skilldoc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ReadName reads a Skill document and validates its name and required description.
func ReadName(path string) (string, error) {
	document, err := Read(path)
	if err != nil {
		return "", err
	}
	if err := Validate(document); err != nil {
		return "", err
	}
	return strings.TrimSpace(document.Name), nil
}

// NameError reports a portability violation, not an unreadable document.
// Installed host packages retain their identity even when their names differ
// from the portable Skill format. New installations still reject this error.
type NameError struct{ Message string }

func (e *NameError) Error() string { return e.Message }

// Validate checks required metadata and portable naming without reading files.
func Validate(document Document) error {
	name := strings.TrimSpace(document.Name)
	if name == "" {
		return errors.New("missing name")
	}
	if strings.TrimSpace(document.Description) == "" {
		return errors.New("missing description")
	}
	if len(name) > 64 {
		return &NameError{Message: "name exceeds 64 characters"}
	}
	if !ValidName(name) {
		return &NameError{Message: fmt.Sprintf("invalid name %q: use lowercase letters, numbers, and hyphens", name)}
	}
	return nil
}

// ErrMissingFrontMatter identifies documents without an opening YAML front-matter delimiter.
var ErrMissingFrontMatter = errors.New("missing YAML front matter")

// Document holds a Skill document header and its provider metadata.
type Document struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Metadata    map[string]any `yaml:"metadata"`
}

// Read parses YAML front matter without changing the file.
func Read(path string) (Document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read skill: %w", err)
	}
	return Parse(content)
}

// Parse reads a document directly from immutable source bytes.
func Parse(content []byte) (Document, error) {
	frontMatter, err := FrontMatter(content)
	if err != nil {
		return Document{}, err
	}
	var document Document
	if err := yaml.Unmarshal(frontMatter, &document); err != nil {
		return Document{}, fmt.Errorf("invalid YAML front matter: %w", err)
	}
	return document, nil
}

// FrontMatter extracts the YAML header while preserving indented delimiter text.
func FrontMatter(content []byte) ([]byte, error) {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	if len(lines) == 0 || !bytes.Equal(lines[0], []byte("---")) {
		return nil, ErrMissingFrontMatter
	}
	for index := 1; index < len(lines); index++ {
		if bytes.Equal(lines[index], []byte("---")) {
			return bytes.Join(lines[1:index], []byte("\n")), nil
		}
	}
	return nil, errors.New("unterminated YAML front matter")
}

// MetadataString reads scalar provider metadata without accepting structured values.
func MetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		return ""
	}
}

// MetadataBool reads a boolean or a parseable boolean string from provider metadata.
func MetadataBool(metadata map[string]any, key string) bool {
	value, ok := metadata[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}

// ValidName checks the lowercase, hyphen-separated Skill name syntax.
func ValidName(name string) bool { return skillName.MatchString(name) }
