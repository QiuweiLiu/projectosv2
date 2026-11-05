package verify_test

import (
	"testing"

	"projectos/kernel/internal/verify"
)

func TestCheckCommandRejectsSmuggling(t *testing.T) {
	bad := []string{
		"npm run testevil",
		"npm test --if-present",
		"node -e process.exit(0)",
		"node ../../etc/passwd.js",
		"go test -exec /bin/sh",
		"python3 -m unittest -k anything",
		"python3 -m pytest -p evil",
		"node test.js; rm -rf /",
	}
	for _, cmd := range bad {
		if err := verify.CheckCommand(cmd); err == nil {
			t.Fatalf("%q was accepted", cmd)
		}
	}
	good := []string{
		"node test.js",
		"node ./test/check.mjs",
		"npm test",
		"npm run test",
		"python3 -m unittest",
		"python3 -m unittest discover",
		"go test ./...",
	}
	for _, cmd := range good {
		if err := verify.CheckCommand(cmd); err != nil {
			t.Fatalf("%q was rejected: %v", cmd, err)
		}
	}
}
