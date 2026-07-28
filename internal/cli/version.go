package cli

// Version is the Hydra version. It is set at build time via -ldflags.
// When not overridden, it defaults to "dev".
var Version = "dev"

// Commit is the git commit hash. Set at build time via -ldflags.
var Commit = ""
