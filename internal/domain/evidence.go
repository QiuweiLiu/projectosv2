package domain

import "time"

// Evidence is the audit record for one acceptance decision. It pins the exact
// artifact version the Kernel verified, so a rejection can be explained later
// even after the worktree has moved on.
type Evidence struct {
	ID              string
	RunID           string
	TaskID          TaskID
	ArtifactPath    string
	ArtifactVersion string
	Digest          string
	Accepted        bool
	Reason          string
	At              time.Time
}
