package migrations

import "embed"

// LatestVersion is the newest embedded migration version this binary expects.
const LatestVersion int64 = 202608040001

// FS contains SQL migrations embedded into the application binary.
//
//go:embed *.sql
var FS embed.FS
