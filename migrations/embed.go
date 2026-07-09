// Package migrations embeds the SQL migration files that every service
// applies at startup through platform/migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
