package config

import "embed"

//go:embed crd/*.yaml
var CRDsFS embed.FS
