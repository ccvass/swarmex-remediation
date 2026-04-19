package remediation

import "testing"

func TestDetermineLevel(t *testing.T) {
	r := &Remediator{}
	tests := []struct {
		count, threshold int
		want             EscalationLevel
	}{
		{5, 5, LevelRestart},
		{9, 5, LevelRestart},
		{10, 5, LevelForceRestart},
		{14, 5, LevelForceRestart},
		{15, 5, LevelDrainNode},
		{20, 5, LevelDrainNode},
	}
	for _, tt := range tests {
		got := r.determineLevel(tt.count, tt.threshold)
		if got != tt.want {
			t.Errorf("determineLevel(%d, %d) = %s, want %s", tt.count, tt.threshold, got, tt.want)
		}
	}
}

func TestParseThreshold(t *testing.T) {
	tests := []struct {
		input string
		want  int
		ok    bool
	}{
		{"5", 5, true},
		{"", 0, false},
		{"bad", 0, false},
		{"0", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseThreshold(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseThreshold(%q) = (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
