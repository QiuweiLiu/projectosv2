package aoenv_test

import (
	"testing"

	"projectos/kernel/internal/aoenv"
)

func TestSetupRejectsEmptyDir(t *testing.T) {
	if _, err := aoenv.Setup(""); err == nil {
		t.Fatal("Setup(\"\") must not create a repository in the current directory")
	}
}
