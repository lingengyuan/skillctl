package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var errMissingFrontMatter = errors.New("missing YAML front matter")

type skillDocument struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Metadata    map[string]any `yaml:"metadata"`
}

func readSkillDocument(path string) (skillDocument, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return skillDocument{}, fmt.Errorf("read skill: %w", err)
	}
	frontMatter, err := extractFrontMatter(content)
	if err != nil {
		return skillDocument{}, err
	}
	var document skillDocument
	if err := yaml.Unmarshal(frontMatter, &document); err != nil {
		return skillDocument{}, fmt.Errorf("invalid YAML front matter: %w", err)
	}
	return document, nil
}

func extractFrontMatter(content []byte) ([]byte, error) {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	if len(lines) == 0 || !bytes.Equal(lines[0], []byte("---")) {
		return nil, errMissingFrontMatter
	}
	for index := 1; index < len(lines); index++ {
		if bytes.Equal(lines[index], []byte("---")) {
			return bytes.Join(lines[1:index], []byte("\n")), nil
		}
	}
	return nil, errors.New("unterminated YAML front matter")
}

func metadataString(metadata map[string]any, key string) string {
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

func metadataBool(metadata map[string]any, key string) bool {
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
