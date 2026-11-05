package ao

import (
	"strings"
	"testing"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

func TestParseFindingsAcceptsCodeFencesAndUnknownSeverity(t *testing.T) {
	text := "```json\n{\"findings\":[{\"severity\":\"p1\",\"code\":\"extra_file\",\"message\":\"extra\"},{\"severity\":\"weird\",\"code\":\"x\"}]}\n```"
	findings, err := parseFindings(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings", len(findings))
	}
	if findings[0].Severity != domain.SeverityP1 || findings[0].Code != "extra_file" {
		t.Fatalf("first = %#v", findings[0])
	}
	if findings[1].Severity != domain.SeverityP1 {
		t.Fatalf("unknown severity must fail closed as P1, got %s", findings[1].Severity)
	}
}

func TestParseFindingsRejectsProse(t *testing.T) {
	if _, err := parseFindings("I think it is fine"); err == nil {
		t.Fatal("prose must not be parsed as a review")
	}
}

func TestFailClosedUsesP1(t *testing.T) {
	findings := failClosed("reviewer_timeout", errString("boom"))
	if len(findings) != 1 || findings[0].Severity != domain.SeverityP1 {
		t.Fatalf("fail-closed findings = %#v", findings)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestReviewPromptPutsContentInTheContentBlock(t *testing.T) {
	req := exec.ReviewRequest{
		Task:     domain.Task{ID: "A", Objective: "produce 42", ArtifactPath: "a.txt"},
		Artifact: exec.Artifact{Result: "42", Files: []string{"a.txt", "notes.md"}},
	}
	prompt := reviewPrompt(req)
	if !strings.Contains(prompt, "<<<CONTENT\n42\nCONTENT>>>") {
		t.Fatalf("content block is wrong:\n%s", prompt)
	}
	if strings.Contains(prompt, "<<<CONTENT\na.txt") {
		t.Fatal("the file list leaked into the content block")
	}
}

func TestParseFindingsRejectsMissingOrNullField(t *testing.T) {
	for _, body := range []string{`{}`, `{"findings":null}`, `{"result":"ok"}`} {
		if _, err := parseFindings(body); err == nil {
			t.Fatalf("%s must be rejected", body)
		}
	}
}

func TestMatchesChatHostUsesExactTokens(t *testing.T) {
	cmd := "/x/ao chat-host repo-1 /tmp/run-1 /tmp/run-1/worktrees/repo/repo-1 acp token -- opencode acp"
	if !matchesChatHost(cmd, "repo-1", "/tmp/run-1") {
		t.Fatal("exact match should succeed")
	}
	if matchesChatHost(cmd, "repo-1", "/tmp/run-10") {
		t.Fatal("/tmp/run-10 must not match /tmp/run-1")
	}
	if matchesChatHost(cmd, "repo-10", "/tmp/run-1") {
		t.Fatal("session id must match exactly")
	}
}

func TestMatchesChatHostHandlesSpacesInDataDir(t *testing.T) {
	cmd := "/x/ao chat-host repo-1 /Users/me/My Runs/run-1 /Users/me/My Runs/run-1/worktrees/repo/repo-1 acp tok -- opencode acp"
	if !matchesChatHost(cmd, "repo-1", "/Users/me/My Runs/run-1") {
		t.Fatal("data dir with spaces must still match")
	}
	if matchesChatHost(cmd, "repo-1", "/Users/me/My Runs/run-10") {
		t.Fatal("run-10 must not match run-1")
	}
}

func TestMatchesChatHostRejectsPrefixCollision(t *testing.T) {
	cmd := "/x/ao chat-host repo-1 /tmp/project old /tmp/project old/worktrees/w acp tok"
	if matchesChatHost(cmd, "repo-1", "/tmp/project") {
		t.Fatal("/tmp/project must not match the longer /tmp/project old")
	}
	if !matchesChatHost(cmd, "repo-1", "/tmp/project old") {
		t.Fatal("exact longer data dir should match")
	}
}

func TestMatchesChatHostReapAll(t *testing.T) {
	cmd := "/x/ao chat-host repo-9 /tmp/run-2 /tmp/run-2/worktrees/repo/repo-9 acp tok"
	if !matchesChatHost(cmd, "", "/tmp/run-2") {
		t.Fatal("ReapAll (empty session) must match a normal chat-host line")
	}
	if matchesChatHost(cmd, "", "/tmp/run-20") {
		t.Fatal("ReapAll must not match a longer prefix")
	}
}
