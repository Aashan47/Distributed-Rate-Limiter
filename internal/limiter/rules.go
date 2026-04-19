package limiter

// RuleRegistry resolves a rule name to its configured limit / window.
type RuleRegistry interface {
	Lookup(name string) (Rule, bool)
	All() []Rule
}

// StaticRules is an immutable, in-memory registry loaded at startup.
type StaticRules struct {
	byName map[string]Rule
}

func NewStaticRules(rules []Rule) *StaticRules {
	byName := make(map[string]Rule, len(rules))
	for _, r := range rules {
		byName[r.Name] = r
	}
	return &StaticRules{byName: byName}
}

func (s *StaticRules) Lookup(name string) (Rule, bool) {
	r, ok := s.byName[name]
	return r, ok
}

func (s *StaticRules) All() []Rule {
	out := make([]Rule, 0, len(s.byName))
	for _, r := range s.byName {
		out = append(out, r)
	}
	return out
}
