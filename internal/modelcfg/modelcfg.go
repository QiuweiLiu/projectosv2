// Package modelcfg stores which model each role should use, plus a cached list
// of models the harness knows about.
package modelcfg

import (
	"bytes"
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
	// Variants holds the provider-specific reasoning effort per role, e.g.
	// {"worker":"high"}. It is composed into the model reference at runtime.
	Variants map[string]string `json:"variants,omitempty"`
}

type Cache struct {
	Models []string  `json:"models"`
	At     time.Time `json:"at"`
}

var (
	valid        = regexp.MustCompile(`^[A-Za-z0-9._/@:-]+$`)
	validVariant = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

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

// SetVariant applies role=variant. An empty value clears the role's variant,
// which restores the model's default reasoning effort.
func (s Settings) SetVariant(role, variant string) (Settings, error) {
	variant = strings.TrimSpace(variant)
	if variant != "" && !validVariant.MatchString(variant) {
		return s, fmt.Errorf("invalid variant %q", variant)
	}
	switch role {
	case "planner", "worker", "reviewer", "acceptance", "reporter", "chat":
	default:
		return s, fmt.Errorf("unknown role %q (want %s)", role, strings.Join(Roles, "|"))
	}
	if s.Variants == nil {
		s.Variants = map[string]string{}
	}
	if variant == "" {
		delete(s.Variants, role)
	} else {
		s.Variants[role] = variant
	}
	return s, nil
}

// VariantFor resolves the effort a role should use, mirroring model fallbacks:
// chat follows worker when no chat model is set; reviewer/acceptance follow the
// worker variant when they inherit the worker model.
func (s Settings) VariantFor(role string) string {
	own := strings.TrimSpace(s.Variants[role])
	switch role {
	case "chat":
		if own != "" || strings.TrimSpace(s.Chat) != "" {
			return own
		}
		return strings.TrimSpace(s.Variants["worker"])
	case "reviewer":
		if strings.TrimSpace(s.Reviewer) == "" && own == "" {
			return strings.TrimSpace(s.Variants["worker"])
		}
	case "acceptance":
		if strings.TrimSpace(s.Acceptance) == "" && own == "" {
			return strings.TrimSpace(s.Variants["worker"])
		}
	}
	return own
}

// WithVariant appends #variant unless the reference already carries one.
func WithVariant(model, variant string) string {
	model = strings.TrimSpace(model)
	variant = strings.TrimSpace(variant)
	if model == "" || variant == "" || strings.Contains(model, "#") {
		return model
	}
	return model + "#" + variant
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

	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 300 {
				msg = msg[:300]
			}
			if msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
			return nil, err
		}
		return out, nil
	}

	parse := func(out []byte) []string {
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
		return models
	}

	// `--standalone` can fail while plugins activate; plain `models` reads the
	// registered providers without starting a session. Plugin activation is
	// occasionally slow right after a harness starts, so retry briefly before
	// falling back, and never let an empty result overwrite a good cache.
	var out []byte
	var err error
	var models []string
	for attempt := 0; attempt < 3; attempt++ {
		out, err = run("models")
		if err == nil {
			models = parse(out)
			if len(models) > 0 {
				break
			}
		}
		if attempt < 2 {
			time.Sleep(time.Duration(700*(attempt+1)) * time.Millisecond)
		}
	}
	if len(models) == 0 {
		if stand, standErr := run("--standalone", "models"); standErr == nil {
			models = parse(stand)
		} else if err == nil {
			err = standErr
		}
	}
	if len(models) == 0 {
		if err == nil {
			err = fmt.Errorf("no models returned")
		}
		return nil, fmt.Errorf("list models: %w", err)
	}
	raw, err := json.MarshalIndent(Cache{Models: models, At: time.Now().UTC()}, "", "  ")
	if err != nil {
		return models, err
	}
	if err := os.WriteFile(cachePath(dir), append(raw, '\n'), 0o644); err != nil {
		return models, err
	}
	return models, nil
}
