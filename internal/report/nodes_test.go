package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/nodes"
)

func TestScanTextNodesGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, w2Scan(t), nodesSample(t)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nodes:", "SOURCE", "NAME", "VERSION", "AMI", "RELEASE", "OS IMAGE", "SEVERITY", "REASONS", "workers-al2", "AL2_x86_64"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, b.String())
		}
	}
	golden(t, "scan-nodes.txt", b.Bytes())
}

func TestScanJSONNodesGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, w2Scan(t), nodesSample(t)); err != nil {
		t.Fatal(err)
	}
	var doc struct{ Nodes []map[string]any }
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil || len(doc.Nodes) != 4 || doc.Nodes[0]["amiType"] != "AL2_x86_64" {
		t.Fatalf("nodes = %+v err=%v", doc.Nodes, err)
	}
	golden(t, "scan-nodes.json", b.Bytes())
}

func TestNodesOmittedWhenEmpty(t *testing.T) {
	var txt, js, plainTxt, plainJS bytes.Buffer
	ex := Extras{Nodes: []nodes.Row{}}
	_ = ScanText(&txt, w2Scan(t), ex)
	_ = ScanJSON(&js, w2Scan(t), ex)
	_ = ScanText(&plainTxt, w2Scan(t), Extras{})
	_ = ScanJSON(&plainJS, w2Scan(t), Extras{})
	if !bytes.Equal(txt.Bytes(), plainTxt.Bytes()) || !bytes.Equal(js.Bytes(), plainJS.Bytes()) {
		t.Errorf("empty nodes changed the output:\n%s\n%s", txt.String(), js.String())
	}
}
