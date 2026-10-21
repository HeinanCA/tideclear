package report

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
)

var update = flag.Bool("update", false, "rewrite golden files")

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

func ip(n int) *int { return &n }

func sample() []eksinfo.Cluster {
	return []eksinfo.Cluster{
		{Name: "demo-a", Region: "us-east-1", Version: "1.36", StandardEnd: d(2027, 8, 2), ExtendedEnd: d(2028, 8, 2), DaysLeft: ip(287), Support: eksinfo.SupportStandard},
		{Name: "demo-b", Region: "eu-west-1", Version: "1.31", StandardEnd: d(2025, 11, 26), ExtendedEnd: d(2026, 11, 26), DaysLeft: ip(38), Support: eksinfo.SupportExtended},
		{Name: "demo-c", Region: "eu-west-1", Version: "1.25", StandardEnd: d(2024, 5, 1), ExtendedEnd: d(2025, 5, 1), DaysLeft: ip(-536), Support: eksinfo.SupportUnsupported},
		{Name: "demo-d", Region: "eu-west-1", Version: "9.99", Support: eksinfo.SupportUnknown},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestClustersTextGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ClustersText(&b, sample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "clusters.txt", b.Bytes())
}

func TestClustersTextEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := ClustersText(&b, nil); err != nil {
		t.Fatal(err)
	}
	if b.String() != "no clusters found\n" {
		t.Fatalf("got %q", b.String())
	}
}

func TestClustersJSONGolden(t *testing.T) {
	var b bytes.Buffer
	sk := []eksinfo.SkippedRegion{{Region: "ap-east-1", Err: errors.New("opt-in required")}}
	if err := ClustersJSON(&b, sample(), sk); err != nil {
		t.Fatal(err)
	}
	golden(t, "clusters.json", b.Bytes())
}

func TestClustersJSONEmptyArrays(t *testing.T) {
	var b bytes.Buffer
	if err := ClustersJSON(&b, nil, nil); err != nil {
		t.Fatal(err)
	}
	golden(t, "clusters-empty.json", b.Bytes())
}
