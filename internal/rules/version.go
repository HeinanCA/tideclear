package rules

import (
	"fmt"
	"regexp"
	"strconv"
)

// Version is a Kubernetes major.minor version.
type Version struct{ Major, Minor int }

var versionRE = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\+|\.(0|[1-9][0-9]*))?$`)

// ParseVersion accepts "v1.25.0", "1.25" and "1.31+"; the patch part and a trailing "+" are dropped.
// Parts must be plain decimal digits without leading zeros.
func ParseVersion(s string) (Version, error) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("invalid version %q: want [v]MAJOR.MINOR[.PATCH|+]", s)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return Version{}, fmt.Errorf("invalid major in version %q: %w", s, err)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return Version{}, fmt.Errorf("invalid minor in version %q: %w", s, err)
	}
	return Version{Major: major, Minor: minor}, nil
}

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// Next returns the following minor version.
func (v Version) Next() Version { return Version{Major: v.Major, Minor: v.Minor + 1} }

func (v Version) before(o Version) bool {
	return v.Major < o.Major || (v.Major == o.Major && v.Minor < o.Minor)
}

// ValidateTarget allows target == current or current+1 only.
func ValidateTarget(current, target Version) error {
	if target.before(current) {
		return fmt.Errorf("target %s is older than current version %s", target, current)
	}
	if current.Next().before(target) {
		return fmt.Errorf("target %s is more than one minor ahead of current version %s (max %s)", target, current, current.Next())
	}
	return nil
}
