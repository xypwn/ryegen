package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	OutDir         string      `toml:"out-dir"`
	Package        string      `toml:"package"`
	Version        string      `toml:"version"`
	CutNew         bool        `toml:"cut-new"`
	DontBuildFlag  string      `toml:"dont-build-flag,omitempty"`
	Exclude        []string    `toml:"exclude,omitempty"`
	NoPrefix       []string    `toml:"no-prefix,omitempty"`
	CustomPrefixes [][2]string `toml:"custom-prefixes,omitempty"` // {prefix, package}
	IncludeStdLibs []string    `toml:"include-std-libs"`
}

func ReadConfigFromFileOrCreateDefault(path string) (cfg *Config, createdDefault bool, err error) {
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, []byte(DefaultConfig("", "", "", "")), 0666); err != nil {
			return nil, false, err
		}
		createdDefault = true
	}
	cfg = &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, false, err
	}
	return
}

func DefaultConfig(outDir, pkg, version, dontBuildFlag string) string {
	if outDir == "" {
		outDir = "../ryegen_bindings"
	}
	if pkg == "" {
		pkg = "github.com/<user>/<repo>"
	}
	if version == "" {
		version = "vX.Y.Z"
	}
	var dontBuildFlagCommentComment, dontBuildFlagLine string
	if dontBuildFlag == "" {
		dontBuildFlagCommentComment = "#"
		dontBuildFlagLine = `#dont-build-flag = "b_no_mygolib"`
	} else {
		dontBuildFlagLine = `dont-build-flag = "` + dontBuildFlag + `"`
	}

	return fmt.Sprintf(
		`# Output directory (relative).
out-dir = "%v"
# Go name of package.
package = "%v"
# Go semantic version of package.
version = "%v"
# Auto-remove "New" part of functions (e.g. widget.NewLabel => widget-label, app.New => app).
cut-new = true

%v# Add a build flag to exclude the binding (optional).
%v

## Exclude listed packages from parsing. This may be useful if a repo
## contains a module not meant to be imported, which would cause
## an error if parsed.
#exclude = [
#  "github.com/<user>/<repo>/non_runnable_code_snippets",
#]

## Descending priority. Packages not listed will always be prefixed.
## In case of conflicting function names, only the function from the
## package with the highest priority is not prefixed.
#no-prefix = [
#  "github.com/<user>/<repo>",
#  "github.com/<user>/<repo>/important",
#]

## Set custom prefix for all symbols in the package (if applicable: see "no-prefix").
#custom-prefixes = [
#  ["my-fyne", "fyne.io/fyne/v2"],
#  ["my-widget", "fyne.io/fyne/v2/widget"],
#]

## Generate bindings for selected parts of the go standard library.
#include-std-libs = [
#  "image",
#]`,
		outDir, pkg, version, dontBuildFlagCommentComment, dontBuildFlagLine,
	)
}
