package cli

import "fmt"

type outputFormat int

const (
	outputText outputFormat = iota
	outputJSON
)

// parseOutput maps an --output value to a format; "table" is kept as an alias
// of "text" and "" means the default (text).
func parseOutput(v string) (outputFormat, error) {
	switch v {
	case "", "text", "table":
		return outputText, nil
	case "json":
		return outputJSON, nil
	}
	return 0, fmt.Errorf("unknown --output %q (want text or json)", v)
}
