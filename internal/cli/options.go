package cli

import (
	"time"
)

// Options contains command-line flags without reading or writing local state.
type Options struct {
	Paths       []string
	ConfigPath  string
	Names       []string
	Hosts       []string
	Scopes      []string
	Help        bool
	Source      string
	Ref         string
	SkillPath   string
	FromHistory bool
	JSON        bool
	DryRun      bool
	AllMatches  bool
	Fix         bool
	Timeout     time.Duration
	JSONVersion int
	Project     string
	Skills      []string
	Copy        bool
	Offline     bool
	Frozen      bool
	File        string
	Profile     string
	Output      string
	NoHistory   bool
	Package     bool
}
