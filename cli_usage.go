package main

import (
	"fmt"
	"io"
)

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  skillctl check [options] [skill...]
  skillctl update [options] [skill...]
  skillctl list [options] [skill...]
  skillctl doctor [options]
  skillctl track [options] skill
  skillctl --version

Commands:
  check               check installed skills for updates
  update              safely update installed skills
  list                list installed skills without network access
  doctor              diagnose local skill and source state
  track               register the source of one copied skill

Options:
  --path PATH         replace configured roots; repeatable
  --config FILE       use a specific configuration file
  --host HOST         include only one host; repeatable
  --scope SCOPE       include only one scope; repeatable
  --timeout DURATION  set each network operation timeout, for example 30s
  --json              emit structured JSON for check, update, list, or doctor
  --all-matches       operate on every installation sharing a requested name
  --help              show this help

Update options:
  --dry-run           report the update plan without changing files

Doctor options:
  --fix               remove stale tracked-source entries after verification

Track options:
  --source SOURCE     Git URL or local repository path
  --ref REF           branch, tag, or commit
  --skill-path PATH   repository-relative skill path
  --from-history      recover sources from trusted Codex/Claude install records

Options must appear before skill names.

Examples:
  skillctl check
  skillctl check --json
  skillctl list --host codex
  skillctl update --dry-run
  skillctl update obsidian-assistant
  skillctl doctor
  skillctl doctor --fix
  skillctl track --source https://github.com/example/skills.git --skill-path skills/example example
  skillctl track --from-history
  skillctl track --from-history example
`)
}
