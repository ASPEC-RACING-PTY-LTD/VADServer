// Package deploy embeds deployment files the server reads at run time.
//
// env.schema.json is the single source of truth for VaultDock environment
// variables (install.sh, .env.example and GET /admin/deploy/env, api-v1
// §11A). Go embed cannot reach parent directories, so the server embeds it
// from here instead of keeping a copy under internal/.
package deploy

import _ "embed"

// EnvSchema is the raw content of deploy/env.schema.json.
//
//go:embed env.schema.json
var EnvSchema []byte
