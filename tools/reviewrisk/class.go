package main

import (
	"encoding/json"
	"fmt"
)

// Class is the PR review weight of a file, mirroring the H/M/A/NONE classes in
// docs/architecture.ja.md. ClassUnknown is the zero value and marks a path no
// rule matched, so a stray Rule{} fails closed to the highest review weight.
type Class int

const (
	ClassUnknown Class = iota
	ClassNone
	ClassA
	ClassM
	ClassH
)

// String renders the class for JSON and the report's class column.
func (c Class) String() string {
	switch c {
	case ClassNone:
		return "NONE"
	case ClassA:
		return "A"
	case ClassM:
		return "M"
	case ClassH:
		return "H"
	default:
		return "?"
	}
}

// MarshalJSON emits the class as its short label ("H"/"M"/"A"/"NONE"/"?").
func (c Class) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

// Level is the risk level reported for the whole diff. The ordering
// none<low<medium<high<critical drives aggregation (the max wins).
type Level int

const (
	LevelNone Level = iota
	LevelLow
	LevelMedium
	LevelHigh
	LevelCritical
)

// String renders the level for JSON, the review:<level> label, and text output.
func (l Level) String() string {
	switch l {
	case LevelNone:
		return "none"
	case LevelLow:
		return "low"
	case LevelMedium:
		return "medium"
	case LevelHigh:
		return "high"
	case LevelCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the level as its lowercase name.
func (l Level) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.String())
}

// levelForClass maps a file's review class to its base risk level. ClassUnknown
// fails closed to high: an unmatched path is never quietly treated as low risk.
func levelForClass(c Class) Level {
	switch c {
	case ClassNone:
		return LevelNone
	case ClassA:
		return LevelLow
	case ClassM:
		return LevelMedium
	case ClassH:
		return LevelHigh
	default: // ClassUnknown
		return LevelHigh
	}
}

// parseLevel resolves the --fail-at argument to a Level.
func parseLevel(s string) (Level, error) {
	switch s {
	case "none":
		return LevelNone, nil
	case "low":
		return LevelLow, nil
	case "medium":
		return LevelMedium, nil
	case "high":
		return LevelHigh, nil
	case "critical":
		return LevelCritical, nil
	default:
		return LevelNone, fmt.Errorf("unknown level %q (want none|low|medium|high|critical)", s)
	}
}

// levelGuidance is the next-action sentence printed per level. The tool never
// calls an LLM; medium and above only point the reader at /code-review.
var levelGuidance = map[Level]string{
	LevelNone:     "レビュー不要(docs のみ)。CI green でマージ可",
	LevelLow:      "AI レビュー(/code-review)で可",
	LevelMedium:   "/code-review + M ファイルを人間が斜め読み",
	LevelHigh:     "人間レビュー必須。AI は補助",
	LevelCritical: "人間精読必須(検証系・ガード系への接触)",
}
