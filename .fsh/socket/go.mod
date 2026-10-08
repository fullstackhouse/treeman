// A manifest whose only job is to make Socket analyse upstream treeman's OWN code.
//
// Socket's GitHub App scores the DEPENDENCIES declared in a repo's manifests. In this
// repo treeman is the root module, so its 65k lines — the ones we decided not to read
// on every sync — are first-party and invisible to it. Declaring the upstream module
// as a dependency here puts it in a dependency graph, where Socket's Go analysis
// (obfuscation, backdoors, `exec.Command` misuse) applies to it.
//
// Nothing builds or imports this. It is a nested module, so `go build ./...` and the
// release workflow's explicit `./internal/... ./pkg/... ./cmd/...` never see it. Bump
// the version when we rebase onto a new upstream tag; the point is that the version
// Socket reports on is the version we are about to build.
module github.com/fullstackhouse/treeman/.fsh/socket

go 1.27.0

require github.com/stubbedev/treeman v1.4.1-0.20261008073000-55dcdd5f6c0d // upstream master 55dcdd5f
