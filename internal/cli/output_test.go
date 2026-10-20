package cli

import "testing"

func TestParseOutput(t *testing.T) {
	for in, want := range map[string]outputFormat{"": outputText, "text": outputText, "table": outputText, "json": outputJSON} {
		got, err := parseOutput(in)
		if err != nil || got != want {
			t.Errorf("parseOutput(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseOutput("x"); err == nil || err.Error() != `unknown --output "x" (want text or json)` {
		t.Errorf("err = %v", err)
	}
}
