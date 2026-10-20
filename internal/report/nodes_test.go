package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/rules"
)

func nodesSample() Extras {
	return Extras{Nodes: []nodes.Row{
		{Source: "nodegroup", Name: "workers-al2", Version: "1.31", AMIType: "AL2_x86_64", ReleaseVersion: "1.31.4-20250101",
			Severity: rules.SeverityBlocker, Reasons: []string{"Amazon Linux 2 (amiType AL2_x86_64) is cgroup v1: kubelet 1.35+ will not start on it and EKS AL2 AMIs stop at 1.32, so these nodes cannot follow the control plane to 1.35"}},
		{Source: "node", Name: "ip-10-0-0-7", Version: "v1.27.9-eks-5e0fdde", OSImage: "Amazon Linux 2",
			Severity: rules.SeverityWarning, Reasons: []string{"kubelet 1.27 is 4 minors behind the control plane 1.31 (max 3)", "version note"}},
		{Source: "node", Name: "ip-10-0-0-9", Version: "weird", OSImage: "Bottlerocket OS 1.30.0",
			Severity: rules.SeverityWarning, Reasons: []string{"version unknown"}},
	}}
}

func TestScanTextNodesGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, scanSample(t), nodesSample()); err != nil {
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
	if err := ScanJSON(&b, scanSample(t), nodesSample()); err != nil {
		t.Fatal(err)
	}
	var doc struct{ Nodes []map[string]any }
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil || len(doc.Nodes) != 3 || doc.Nodes[0]["amiType"] != "AL2_x86_64" {
		t.Fatalf("nodes = %+v err=%v", doc.Nodes, err)
	}
	golden(t, "scan-nodes.json", b.Bytes())
}

func TestNodesOmittedWhenEmpty(t *testing.T) {
	var txt, js, plainTxt, plainJS bytes.Buffer
	ex := Extras{Nodes: []nodes.Row{}}
	_ = ScanText(&txt, scanSample(t), ex)
	_ = ScanJSON(&js, scanSample(t), ex)
	_ = ScanText(&plainTxt, scanSample(t), Extras{})
	_ = ScanJSON(&plainJS, scanSample(t), Extras{})
	if !bytes.Equal(txt.Bytes(), plainTxt.Bytes()) || !bytes.Equal(js.Bytes(), plainJS.Bytes()) {
		t.Errorf("empty nodes changed the output:\n%s\n%s", txt.String(), js.String())
	}
}
