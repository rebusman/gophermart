package migrations

import "embed"

// FS содержит файлы каталога миграций, встроенные в бинарник.
//
//go:embed *
var FS embed.FS
