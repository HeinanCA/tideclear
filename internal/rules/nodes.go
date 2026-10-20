package rules

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/yaml"
)

//go:embed data/nodes.yaml
var rawNodes []byte

// NodeSource is a documentation page a node rule rests on.
type NodeSource struct{ URL, Note, Fetched string }

// NodeRules is the node and AMI rule data (data/nodes.yaml).
type NodeRules struct {
	AMIFamilyPrefixes     []string
	OSImage               *regexp.Regexp
	CgroupV2Required      Version
	FargateLabelKey       string
	FargateLabelValue     string
	ManagedNodegroupLabel string
	MaxMinorsOlder        int
	Sources               []NodeSource
}

type rawNodeRules struct {
	AMIFamilyPrefixes []string `json:"amiFamilyPrefixes"`
	OSImagePattern    string   `json:"osImagePattern"`
	CgroupV2Required  string   `json:"cgroupV2Required"`
	FargateLabel      struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"fargateLabel"`
	ManagedNodegroupLabelKey string `json:"managedNodegroupLabelKey"`
	Skew                     struct {
		MaxMinorsOlder int `json:"maxMinorsOlder"`
	} `json:"skew"`
	Sources []struct {
		URL     string `json:"url"`
		Fetched string `json:"fetched"`
		Note    string `json:"note"`
	} `json:"sources"`
}

// LoadNodes returns the embedded node rules.
func LoadNodes() (*NodeRules, error) {
	nr, err := parseNodes(rawNodes)
	if err != nil {
		return nil, fmt.Errorf("node rules: %w", err)
	}
	return nr, nil
}

// parseNodes strictly decodes b: unknown keys, a bad pattern or version, or a missing field is an error.
func parseNodes(b []byte) (*NodeRules, error) {
	j, err := yaml.ToJSON(b)
	if err != nil {
		return nil, fmt.Errorf("convert yaml: %w", err)
	}
	var raw rawNodeRules
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw.AMIFamilyPrefixes) == 0 || raw.OSImagePattern == "" || raw.FargateLabel.Key == "" ||
		raw.ManagedNodegroupLabelKey == "" || raw.Skew.MaxMinorsOlder <= 0 || len(raw.Sources) == 0 {
		return nil, errors.New("amiFamilyPrefixes, osImagePattern, fargateLabel, managedNodegroupLabelKey, skew and sources are all required")
	}
	re, err := regexp.Compile(raw.OSImagePattern)
	if err != nil {
		return nil, fmt.Errorf("osImagePattern: %w", err)
	}
	cg, err := ParseVersion(raw.CgroupV2Required)
	if err != nil {
		return nil, fmt.Errorf("cgroupV2Required: %w", err)
	}
	nr := &NodeRules{
		AMIFamilyPrefixes: raw.AMIFamilyPrefixes, OSImage: re, CgroupV2Required: cg,
		FargateLabelKey: raw.FargateLabel.Key, FargateLabelValue: raw.FargateLabel.Value,
		ManagedNodegroupLabel: raw.ManagedNodegroupLabelKey, MaxMinorsOlder: raw.Skew.MaxMinorsOlder,
	}
	for _, s := range raw.Sources {
		if s.URL == "" || s.Fetched == "" {
			return nil, errors.New("sources: url and fetched are required")
		}
		nr.Sources = append(nr.Sources, NodeSource{URL: s.URL, Note: s.Note, Fetched: s.Fetched})
	}
	return nr, nil
}

// IsAL2AMIType reports whether a managed nodegroup amiType is an Amazon Linux 2 family.
func (nr *NodeRules) IsAL2AMIType(amiType string) bool {
	for _, p := range nr.AMIFamilyPrefixes {
		if strings.HasPrefix(amiType, p) {
			return true
		}
	}
	return false
}

// IsAL2OSImage reports whether a node's osImage is Amazon Linux 2 (not 2023).
func (nr *NodeRules) IsAL2OSImage(osImage string) bool { return nr.OSImage.MatchString(osImage) }

// AL2Severity is how an AL2 node ranks at target: BLOCKER once the kubelet
// requires cgroup v2, WARNING before.
func (nr *NodeRules) AL2Severity(target Version) Severity {
	if target.before(nr.CgroupV2Required) {
		return SeverityWarning
	}
	return SeverityBlocker
}

// ParseNodeVersion parses a kubelet or nodegroup version: a leading "v" is
// dropped and everything from the first '-' or '+' is cut, so
// "v1.31.4-eks-5e0fdde" is 1.31. Never use it on a releaseVersion (display only).
func ParseNodeVersion(s string) (Version, error) {
	core := strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	v, err := ParseVersion(core)
	if err != nil {
		return Version{}, fmt.Errorf("invalid node version %q", s)
	}
	return v, nil
}
