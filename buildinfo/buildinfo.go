package buildinfo

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
)

const APIVersion = "v1"

// These values are replaced in release/container builds through -ldflags -X.
// Empty values intentionally fall back to Go's embedded VCS metadata.
var (
	Version   string
	Commit    string
	BuildTime string
	Modified  string
)

//go:embed VERSION
var defaultVersion string

type Info struct {
	Version    string `json:"version"`
	APIVersion string `json:"api_version"`
	Commit     string `json:"commit"`
	BuildTime  string `json:"build_time"`
	Modified   string `json:"modified"`
}

func Current() Info {
	info := Info{
		Version: strings.TrimSpace(Version), APIVersion: APIVersion,
		Commit: strings.TrimSpace(Commit), BuildTime: strings.TrimSpace(BuildTime),
		Modified: strings.TrimSpace(Modified),
	}
	if info.Version == "" {
		info.Version = strings.TrimSpace(defaultVersion)
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.BuildTime == "" {
					info.BuildTime = setting.Value
				}
			case "vcs.modified":
				if info.Modified == "" {
					info.Modified = setting.Value
				}
			}
		}
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.BuildTime == "" {
		info.BuildTime = "unknown"
	}
	if info.Modified == "" {
		info.Modified = "unknown"
	}
	return info
}

func Write(component string, output io.Writer) error {
	if strings.TrimSpace(component) == "" || output == nil {
		return fmt.Errorf("version output requires component and writer")
	}
	info := Current()
	_, err := fmt.Fprintf(output, "%s %s api=%s commit=%s built=%s modified=%s\n",
		component, info.Version, info.APIVersion, info.Commit, info.BuildTime, info.Modified)
	return err
}

func WriteJSON(output io.Writer) error {
	if output == nil {
		return fmt.Errorf("version output requires writer")
	}
	return json.NewEncoder(output).Encode(Current())
}

func Requested(arguments []string) bool {
	return len(arguments) == 1 && (arguments[0] == "version" || arguments[0] == "--version")
}
