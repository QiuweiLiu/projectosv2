// Package policy holds domain capability packs. The Kernel itself must stay
// domain-neutral: everything that mentions a field of work lives here.
package policy

import "fmt"

// Pack is a bundle of execution defaults for one kind of work.
type Pack struct {
	Name          string
	Description   string
	RequireReview bool
	MaxAttempts   int
}

var builtin = map[string]Pack{
	"simple": {
		Name:        "simple",
		Description: "one attempt, no reviewer: for cheap, low-risk artifacts",
		MaxAttempts: 1,
	},
	"research": {
		Name:          "research",
		Description:   "review required, two attempts: artifacts must survive an independent check",
		RequireReview: true,
		MaxAttempts:   2,
	},
}

// Default is used when an objective does not name a pack.
const Default = "simple"

func Get(name string) (Pack, error) {
	if name == "" {
		name = Default
	}
	pack, ok := builtin[name]
	if !ok {
		return Pack{}, fmt.Errorf("unknown policy pack %q", name)
	}
	return pack, nil
}

func Names() []string {
	out := make([]string, 0, len(builtin))
	for name := range builtin {
		out = append(out, name)
	}
	return out
}
