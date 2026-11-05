package objective

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `{
  "name": "demo",
  "tasks": [
    {"id":"A","objective":"produce 42","prompt":"p","artifact":"a.txt","validator":{"type":"exact","expect":"42"}},
    {"id":"B","objective":"produce 7","prompt":"p","artifact":"b.txt","validator":{"type":"exact","expect":"7"}},
    {"id":"C","objective":"sum","prompt":"p ${A} ${B}","artifact":"c.txt","deps":["A","B"],"validator":{"type":"exact","expect":"49"}}
  ]
}`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "objective.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndCompileValidators(t *testing.T) {
	obj, err := Load(write(t, sample))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(obj.Tasks) != 3 {
		t.Fatalf("tasks = %d", len(obj.Tasks))
	}
	checks := obj.Validators()
	if err := checks["A"]("42", nil); err != nil {
		t.Fatalf("A validator: %v", err)
	}
	if err := checks["A"]("43", nil); err == nil {
		t.Fatal("A validator must reject 43")
	}
	tasks := obj.DomainTasks()
	if tasks[2].Validator != "C" || len(tasks[2].Deps) != 2 {
		t.Fatalf("C task = %#v", tasks[2])
	}
}

func TestValidationRejectsBrokenGraph(t *testing.T) {
	cases := map[string]string{
		"empty":       `{"tasks":[]}`,
		"dup id":      `{"tasks":[{"id":"A","validator":{"type":"exact","expect":"1"}},{"id":"A","validator":{"type":"exact","expect":"1"}}]}`,
		"unknown dep": `{"tasks":[{"id":"A","deps":["Z"],"validator":{"type":"exact","expect":"1"}}]}`,
		"self dep":    `{"tasks":[{"id":"A","deps":["A"],"validator":{"type":"exact","expect":"1"}}]}`,
		"bad type":    `{"tasks":[{"id":"A","validator":{"type":"regex","expect":"1"}}]}`,
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestValidationRejectsUnsafeOrAmbiguousSpecs(t *testing.T) {
	cases := map[string]string{
		"cycle":            `{"tasks":[{"id":"A","deps":["B"],"artifact":"a.txt","validator":{"type":"exact","expect":"1"}},{"id":"B","deps":["A"],"artifact":"b.txt","validator":{"type":"exact","expect":"1"}}]}`,
		"absolute path":    `{"tasks":[{"id":"A","artifact":"/etc/passwd","validator":{"type":"exact","expect":"1"}}]}`,
		"parent traversal": `{"tasks":[{"id":"A","artifact":"../x","validator":{"type":"exact","expect":"1"}}]}`,
		"empty artifact":   `{"tasks":[{"id":"A","validator":{"type":"exact","expect":"1"}}]}`,
		"empty expect":     `{"tasks":[{"id":"A","artifact":"a.txt","validator":{"type":"exact"}}]}`,
		"unknown field":    `{"tasks":[{"id":"A","artifact":"a.txt","operation":"rm","validator":{"type":"exact","expect":"1"}}]}`,
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestExactValidatorIsByteExact(t *testing.T) {
	obj, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if err := obj.Validators()["A"]("42\n", nil); err == nil {
		t.Fatal("exact must not accept a trailing newline")
	}
	if err := obj.Validators()["A"]("42", nil); err != nil {
		t.Fatalf("exact rejected the exact value: %v", err)
	}
}

func TestArtifactPathAllowsUnicodeAndSpaces(t *testing.T) {
	body := `{"tasks":[{"id":"A","artifact":"结果 文件.txt","validator":{"type":"exact","expect":"ok"}}]}`
	obj, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("unicode/space artifact path should be valid: %v", err)
	}
	if obj.Tasks[0].Artifact != "结果 文件.txt" {
		t.Fatalf("artifact = %q", obj.Tasks[0].Artifact)
	}
}
