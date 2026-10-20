package rules

import (
	"strings"
	"testing"
)

func TestParseNodeVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{"v1.31.4-eks-5e0fdde", Version{1, 31}, false},
		{"1.31", Version{1, 31}, false},
		{"1.31.4-20250101", Version{1, 31}, false},
		{"v1.31.4+eks", Version{1, 31}, false},
		{"garbage", Version{}, true},
		{"", Version{}, true},
		{"v-eks", Version{}, true},
	}
	for _, tc := range tests {
		got, err := ParseNodeVersion(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseNodeVersion(%q) = %v, %v; want %v, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestLoadNodesData(t *testing.T) {
	nr, err := LoadNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nr.AMIFamilyPrefixes) != 1 || nr.AMIFamilyPrefixes[0] != "AL2_" {
		t.Errorf("AMIFamilyPrefixes = %v, want [AL2_]", nr.AMIFamilyPrefixes)
	}
	if nr.LastAL2AMIVersion != (Version{1, 32}) {
		t.Errorf("LastAL2AMIVersion = %v, want 1.32", nr.LastAL2AMIVersion)
	}
	if nr.CgroupV2Required != (Version{1, 35}) || nr.MaxMinorsOlder != 3 {
		t.Errorf("cgroup=%v skew=%d", nr.CgroupV2Required, nr.MaxMinorsOlder)
	}
	if nr.FargateLabelKey != "eks.amazonaws.com/compute-type" || nr.FargateLabelValue != "fargate" || nr.ManagedNodegroupLabel != "eks.amazonaws.com/nodegroup" {
		t.Errorf("labels = %q=%q, %q", nr.FargateLabelKey, nr.FargateLabelValue, nr.ManagedNodegroupLabel)
	}
	want := []string{
		"https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-deprecation-faqs.html",
		"https://kubernetes.io/blog/2026/10/06/kubernetes-cgroups-v2-shift/",
		"https://kubernetes.dev/resources/keps/5573",
		"https://kubernetes.io/releases/version-skew-policy/",
	}
	for _, u := range want {
		found := false
		for _, s := range nr.Sources {
			if s.URL == u && s.Fetched == "2026-10-20" && s.Note != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("source %s with fetch date and note missing: %+v", u, nr.Sources)
		}
	}
}

func TestNodeMatchersDistinguishAL2FromAL2023(t *testing.T) {
	nr, _ := LoadNodes()
	for _, tc := range []struct {
		ami  string
		want bool
	}{{"AL2_x86_64", true}, {"AL2_ARM_64", true}, {"AL2_x86_64_GPU", true}, {"AL2023_x86_64_STANDARD", false}, {"BOTTLEROCKET_x86_64", false}, {"CUSTOM", false}} {
		if got := nr.IsAL2AMIType(tc.ami); got != tc.want {
			t.Errorf("IsAL2AMIType(%q) = %v", tc.ami, got)
		}
	}
	for _, tc := range []struct {
		os   string
		want bool
	}{{"Amazon Linux 2", true}, {"Amazon Linux 2 (Karoo)", true}, {"Amazon Linux 2023.6.20250115", false}, {"Bottlerocket OS 1.30.0 (aws-k8s-1.31)", false}, {"", false}} {
		if got := nr.IsAL2OSImage(tc.os); got != tc.want {
			t.Errorf("IsAL2OSImage(%q) = %v", tc.os, got)
		}
	}
}

func TestAL2SeverityBoundary(t *testing.T) {
	nr, _ := LoadNodes()
	for _, tc := range []struct {
		target Version
		want   Severity
	}{{Version{1, 34}, SeverityWarning}, {Version{1, 35}, SeverityBlocker}, {Version{1, 36}, SeverityBlocker}, {Version{1, 33}, SeverityWarning}} {
		if got := nr.AL2Severity(tc.target); got != tc.want {
			t.Errorf("AL2Severity(%v) = %s, want %s", tc.target, got, tc.want)
		}
	}
}

func TestNodesStrictDecode(t *testing.T) {
	good := string(rawNodes)
	if _, err := parseNodes([]byte(good)); err != nil {
		t.Fatalf("embedded data: %v", err)
	}
	if _, err := parseNodes([]byte(good + "\nbogusField: 1\n")); err == nil || !strings.Contains(err.Error(), "bogusField") {
		t.Errorf("unknown field not rejected: %v", err)
	}
	bad := strings.Replace(good, "^Amazon Linux 2( |$)", "([", 1)
	if _, err := parseNodes([]byte(bad)); err == nil {
		t.Error("bad regex accepted")
	}
	if _, err := parseNodes([]byte(strings.Replace(good, "  value: fargate\n", "  value: \"\"\n", 1))); err == nil {
		t.Error("empty fargateLabel.value accepted")
	}
	if _, err := parseNodes([]byte(strings.Replace(good, "lastAL2AMIVersion: \"1.32\"\n", "", 1))); err == nil {
		t.Error("missing lastAL2AMIVersion accepted")
	}
	if _, err := parseNodes([]byte("amiFamilyPrefixes: []\n")); err == nil {
		t.Error("incomplete data accepted")
	}
}
