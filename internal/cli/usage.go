package cli

import (
	"fmt"
	"io"
)

// Usage writes the command-line reference.
func Usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  skillctl COMMAND [options] [skill name, id, or contentId...]

Inventory:
  list, show          inspect content, ownership, Agent bindings, capabilities
  check               check sources; may save verified provenance and baselines
  diff                compare installed content with its source
  doctor [--fix]      diagnose local state and recover safe interrupted operations

Lifecycle:
  install SOURCE      install Git, local directory, ZIP, tar.gz, or SKILL.md
  enable, disable     add, restore, or suspend selected Agent bindings
  remove              remove selected bindings; retain shared content and history
  update              advance installed skills, preserving owners and local edits
  pin, unpin          fix or release the installed revision
  history [ID]        inspect persistent operations
  rollback [ID]       restore a verified operation (default: latest committed)
  track               explicitly verify the source of an existing copied skill

Environments:
  sync                reconcile skillctl.toml and skillctl.lock
  import [FILE]       sync an exported environment
  export [skill...]   write a portable manifest, lock, and required local artifacts
  profile [list]      list declared profiles
  profile use NAME    sync a profile; keep unrelated/manual installations

Utilities:
  search QUERY        search skills.sh; --source searches a particular source
  config [show]       display effective local configuration without creating it
  config path, hosts  show the config path or supported Agent directories
  config init         explicitly create config.toml
  config gc           remove unreferenced store revisions; retain history references
  completion SHELL    bash, zsh, fish, or powershell
  version, --version  show version

Common options (may appear before or after positional arguments):
  --path PATH         replace discovery roots; repeatable, required paths
  --config FILE       use an explicit local discovery configuration
  --host HOST, -a     select Agent bindings; repeatable (--agent is an alias)
  --scope SCOPE       user or project; repeatable for discovery
  --project PATH     discover/use a particular project
  --global, -g        select user scope
  --timeout DURATION  timeout for each network operation (default 10s)
  --json              preserve v1 output for list/check/update/doctor
  --json-version 2    unified inventory, diagnostics, plan, and operation envelope
  --all-matches       select every content copy with a requested ambiguous name
  --dry-run           plan without installation, config, or provenance writes
  --offline           use local evidence and verified cached artifacts
  --help, -h          show help

Source and environment options:
  --skill NAME, -s    select a source skill for install; repeatable
  --copy              create copies instead of directory links
  --ref REF           branch/tag/commit for install or track
  --skill-path PATH   select a repository-relative skill directory
  --source SOURCE     source for track or search
  --from-history      explicitly recover trusted installer evidence with track
  --no-history        skip automatic source recovery during check/update
  --package           authorize the complete native plugin package operation
  --file FILE         environment manifest (default: project/skillctl.toml)
  --profile NAME      select a declared environment profile
  --frozen            sync/import only exact locked revisions; never resolve latest
  --output FILE, -o   output manifest for export
  --fix               repair verified stale source entries with doctor

Examples:
  skillctl install ./my-skill --host codex --host claude
  skillctl list --json-version 2
  skillctl check --dry-run
  skillctl update example --dry-run
  skillctl disable example --host claude
  skillctl show example
  skillctl sync --file ./skillctl.toml --frozen --offline
  skillctl update --file ./skillctl.toml
  skillctl export example --output ./shared/skillctl.toml
  skillctl import ./shared/skillctl.toml --frozen
  skillctl track --source https://github.com/example/skills.git --skill-path skills/example example
`)
}
