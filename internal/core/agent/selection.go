package agent

import (
	"fmt"
	"strings"
	"unicode"
)

// Selection carries process-level model and effort overrides. Empty fields
// leave the corresponding choice to a lower-priority layer or the agent CLI.
type Selection struct {
	Name   string
	Model  string
	Effort string
}

// ParseSelection accepts name[:model[:effort]], including an empty model.
func ParseSelection(raw string) (Selection, error) {
	parts := strings.Split(raw, ":")
	if len(parts) > 3 {
		return Selection{}, fmt.Errorf("invalid agent selection %q: expected name[:model[:effort]]", raw)
	}
	s := Selection{Name: parts[0]}
	if len(parts) > 1 {
		s.Model = parts[1]
	}
	if len(parts) > 2 {
		s.Effort = parts[2]
	}
	return s, s.Validate()
}

func (s Selection) String() string {
	if s.Effort != "" {
		return s.Name + ":" + s.Model + ":" + s.Effort
	}
	if s.Model != "" {
		return s.Name + ":" + s.Model
	}
	return s.Name
}

// Validate checks syntax and supported flags, leaving provider values to its CLI.
func (s Selection) Validate() error {
	if err := ValidateKnown(s.Name); err != nil {
		return err
	}
	for _, value := range []string{s.Model, s.Effort} {
		if strings.ContainsAny(value, ":\x00") || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
			return fmt.Errorf("invalid agent selection %q: model and effort must not contain whitespace, ':' or NUL", s.String())
		}
	}
	def := registry[s.Name]
	if s.Model != "" && def.ModelFlag == "" {
		return fmt.Errorf("agent %q does not support model selection", s.Name)
	}
	if s.Effort != "" && def.EffortArgs == nil {
		return fmt.Errorf("agent %q does not support effort selection", s.Name)
	}
	return nil
}

// ResolveSelection uses the first name, then fills each field only from layers
// with that name. Layers are ordered from highest to lowest priority.
func ResolveSelection(layers ...Selection) Selection {
	var result Selection
	for _, layer := range layers {
		if result.Name == "" {
			result.Name = layer.Name
		}
		if result.Name == "" || layer.Name != result.Name {
			continue
		}
		if result.Model == "" {
			result.Model = layer.Model
		}
		if result.Effort == "" {
			result.Effort = layer.Effort
		}
	}
	return result
}

func selectionArgs(s Selection) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	def := registry[s.Name]
	var args []string
	if s.Model != "" {
		args = append(args, def.ModelFlag, s.Model)
	}
	if s.Effort != "" {
		args = append(args, def.EffortArgs(s.Effort)...)
	}
	return args, nil
}
