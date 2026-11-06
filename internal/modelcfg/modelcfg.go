// Package modelcfg stores which model each role should use, plus a cached list
// of models the harness knows about.
package modelcfg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Roles are the model-backed parts of the system.
var Roles = []string{"planner", "worker", "reviewer", "acceptance", "reporter", "chat"}

type Settings struct {
	Planner    string `json:"planner,omitempty"`
	Worker     string `json:"worker,omitempty"`
	Reviewer   string `json:"reviewer,omitempty"`
	Acceptance string `json:"acceptance,omitempty"`
	Reporter   string `json:"reporter,omitempty"`
	Chat       string `json:"chat,omitempty"`
}

type Cache struct {
	Models []string  `json:"models"`
	At     time.Time `json:"at"`
}

var valid = regexp.MustCompile(`^[A-Za-z0-9._/@:-]+$`)

// A workspace serves many project directories but keeps one model policy. The
// environment override lets `serve` pass that shared file down to every `goal`
// subprocess without copying settings into each project.
func settingsPath(dir string) string {
	if p := os.Getenv("KERNEL_MODELS_FILE"); p != "" {
		return p
	}
	return filepath.Join(dir, "models.json")
}

func cachePath(dir string) string {
	if p := os.Getenv("KERNEL_MODELS_CACHE"); p != "" {
		return p
	}
	return filepath.Join(dir, "models-cache.json")
}

func Load(dir string) Settings {
	var s Settings
	raw, err := os.ReadFile(settingsPath(dir))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	return s
}

// Set applies role=model pairs. An empty value clears the role.
func (s Settings) Set(role, model string) (Settings, error) {
	model = strings.TrimSpace(model)
	if model != "" && !valid.MatchString(model) {
		return s, fmt.Errorf("invalid model %q", model)
	}
	switch role {
	case "planner":
		s.Planner = model
	case "worker":
		s.Worker = model
	case "reviewer":
		s.Reviewer = model
	case "acceptance":
		s.Acceptance = model
	case "reporter":
		s.Reporter = model
	case "chat":
		s.Chat = model
	default:
		return s, fmt.Errorf("unknown role %q (want %s)", role, strings.Join(Roles, "|"))
	}
	return s, nil
}

func Save(dir string, s Settings) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := settingsPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath(dir))
}

func LoadCache(dir string) Cache {
	var c Cache
	raw, err := os.ReadFile(cachePath(dir))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(raw, &c)
	return c
}

// Refresh asks the harness for its model list and caches it. It is a separate
// command so the UI never blocks on a slow CLI call.
func Refresh(ctx context.Context, dir, bin string) ([]string, error) {
	if bin == "" {
		bin = "opencode"
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--standalone", "models").Output()
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	seen := map[string]bool{}
	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "/") || seen[line] {
			continue
		}
		seen[line] = true
		models = append(models, line)
	}
	sort.Strings(models)
	raw, err := json.MarshalIndent(Cache{Models: models, At: time.Now().UTC()}, "", "  ")
	if err != nil {
		return models, err
	}
	if err := os.WriteFile(cachePath(dir), append(raw, '\n'), 0o644); err != nil {
		return models, err
	}
	return models, nil
}
