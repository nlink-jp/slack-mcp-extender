// Package e2e holds live end-to-end tests that drive the built
// slack-mcp-extender binary against the real Slack MCP endpoint. They are
// guarded by the `e2e` build tag so `go test ./...` (offline) never runs
// them; run them deliberately with:
//
//	make e2e          # or: go test -tags e2e -count=1 ./e2e/...
//
// Requirements:
//
//   - SLACK_MCP_EXTENDER_E2E_CONFIG: path to a logged-in workspace config
//     (run `slack-mcp-extender login` first). Tests are skipped when unset.
//   - SLACK_MCP_EXTENDER_E2E_CHANNEL: channel ID for the posting test.
//     When unset, only the read-only and denial tests run — nothing is
//     ever posted to Slack without it.
//
// This file has no build tag so the package always compiles (and reports
// "no test files" without the tag).
package e2e
