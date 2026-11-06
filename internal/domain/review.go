package domain

import "time"

type Severity string

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
)

type Finding struct {
	Severity Severity
	Code     string
	Message  string
}

type ReviewKind string

const (
	ReviewFull       ReviewKind = "full"
	ReviewTargeted   ReviewKind = "targeted"
	ReviewAcceptance ReviewKind = "acceptance"
	ReviewObjective  ReviewKind = "objective"
)

// Review is one reviewer pass bound to an exact artifact version. It can never
// change Task status by itself.
type Review struct {
	ID              string
	TaskID          TaskID
	ArtifactVersion string
	Kind            ReviewKind
	Findings        []Finding
	PromptChars     int
	OutputChars     int
	PromptTokens    int
	OutputTokens    int
	UsageEstimated  bool
	SessionID       string
	At              time.Time
}

func (r Review) Blocking() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityP0 || f.Severity == SeverityP1 {
			return true
		}
	}
	return false
}
