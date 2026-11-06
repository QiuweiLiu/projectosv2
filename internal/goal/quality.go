package goal

import (
	"fmt"
	"strings"
)

// ValidateImplementationChecks rejects a common planner failure: a contains or
// exact validator asserts an internal symbol (def add_task, class Foo) that the
// task text never asked for. The worker cannot know the requirement, so the
// plan is structurally unfair even though its schema is valid.
func ValidateImplementationChecks(p Plan) error {
	for _, task := range p.Tasks {
		text := strings.ToLower(task.Goal + "\n" + task.Scope + "\n" + strings.Join(task.Acceptance, "\n"))
		for _, v := range task.Validators {
			if v.Type != "contains" && v.Type != "exact" {
				continue
			}
			symbol, ok := codeSymbol(v.Expect)
			if !ok {
				continue
			}
			if !strings.Contains(text, strings.ToLower(symbol)) {
				return fmt.Errorf("task %s validator %s %q names %q, but the task text never mentions it; state the requirement or use a command validator",
					task.ID, v.Type, v.Expect, symbol)
			}
		}
	}
	return nil
}

func codeSymbol(expect string) (string, bool) {
	s := strings.TrimSpace(expect)
	for _, prefix := range []string{"def ", "class ", "function ", "const ", "let ", "var ", "struct ", "interface ", "func "} {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(s, prefix))
		if rest == "" {
			return "", false
		}
		if i := strings.IndexAny(rest, "(:< \t"); i >= 0 {
			rest = rest[:i]
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		return rest, true
	}
	return "", false
}
