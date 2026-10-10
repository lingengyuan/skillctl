module github.com/lingengyuan/skillctl

go 1.27

require (
	github.com/BurntSushi/toml v1.6.0
	gopkg.in/yaml.v3 v3.0.1
)

// The pre-rewrite CLI shipped as v0.0.1-v0.0.3 and v0.1.0-v0.4.0. Those tags
// were deleted from the repository, but module proxies keep serving deleted
// tags, so `go install ...@latest` still resolved the abandoned v0.4.0. Retract
// lets tooling skip them and reach the current release.
retract (
	[v0.0.1, v0.0.3] // pre-rewrite CLI
	[v0.1.0, v0.4.0] // pre-rewrite CLI
)
