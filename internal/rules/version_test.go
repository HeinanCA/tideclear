package rules

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{"v1.25.0", Version{1, 25}, false},
		{"1.25", Version{1, 25}, false},
		{"1.31+", Version{1, 31}, false},
		{"v1.9.0", Version{1, 9}, false},
		{"", Version{}, true},
		{"1", Version{}, true},
		{"x.y", Version{}, true},
		{"1.-2", Version{}, true},
		{"1.+5", Version{}, true},
		{"1.25.x", Version{}, true},
		{"1.2.3.4", Version{}, true},
		{"1.25.0+", Version{}, true},
		{"1.025", Version{}, true},
		{"+1.25", Version{}, true},
		{"v1.25.7", Version{1, 25}, false},
		{"1.0", Version{1, 0}, false},
		{" 1.25", Version{}, true},
	}
	for _, tc := range tests {
		got, err := ParseVersion(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseVersion(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestVersionNext(t *testing.T) {
	if got := (Version{1, 25}).Next(); got != (Version{1, 26}) {
		t.Errorf("Next = %v, want 1.26", got)
	}
}

func TestValidateTarget(t *testing.T) {
	cur := Version{1, 25}
	tests := []struct {
		name    string
		target  Version
		wantErr bool
	}{
		{"equal", Version{1, 25}, false},
		{"plus one", Version{1, 26}, false},
		{"plus two", Version{1, 27}, true},
		{"minus one", Version{1, 24}, true},
	}
	for _, tc := range tests {
		err := ValidateTarget(cur, tc.target)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
