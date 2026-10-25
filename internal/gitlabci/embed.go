package gitlabci

import "embed"

//go:embed assets/*.py assets/*.yml
var Assets embed.FS
