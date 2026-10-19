package report

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/rules"
)

// ScanText writes the cluster header, the findings table and coverage
// footers (skipped groups/kinds, unverified objects). A removal planned after
// the bundled Kubernetes release is marked "(scheduled)".
func ScanText(w io.Writer, r apiscan.Result) error {
	if c := r.Cluster; c.Context != "" || c.Server != "" {
		line := fmt.Sprintf("cluster: %s (%s)", dash(c.Context), dash(c.Server))
		if c.EKSCluster != "" {
			line += fmt.Sprintf(" [EKS: %s]", c.EKSCluster)
		}
		_, _ = fmt.Fprintln(w, line)
	}
	switch {
	case len(r.Findings) > 0:
		if err := findingsTable(w, r); err != nil {
			return err
		}
		if n := len(r.Skipped); n > 0 {
			_, _ = fmt.Fprintf(w, "warning: %d groups/kinds skipped (see stderr); results are partial\n", n)
		}
	case len(r.Skipped) > 0:
		_, _ = fmt.Fprintf(w, "no findings for target %s in scanned resources; %d skipped, results are partial\n", r.Target, len(r.Skipped))
	default:
		_, _ = fmt.Fprintf(w, "no findings for target %s\n", r.Target)
	}
	if r.Unverified > 0 {
		_, _ = fmt.Fprintf(w, "%d objects had no apiVersion evidence and were not checked\n", r.Unverified)
	}
	return nil
}

func findingsTable(w io.Writer, r apiscan.Result) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KIND\tAPIVERSION\tNAMESPACE\tNAME\tREPLACEMENT\tREMOVED IN\tSEVERITY")
	for _, it := range r.Findings {
		rule := it.Finding.Rule
		removed := versionOrDash(rule.RemovedIn)
		if rule.Scheduled() {
			removed += " (scheduled)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			it.Object.Kind, it.Object.APIVersion, dash(it.Object.Namespace), it.Object.Name,
			dash(rule.Replacement), removed, it.Finding.Severity)
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func versionOrDash(v *rules.Version) string {
	if v == nil {
		return "-"
	}
	return v.String()
}

func versionPtr(v *rules.Version) *string {
	if v == nil {
		return nil
	}
	s := v.String()
	return &s
}

type findingJSON struct {
	Kind         string  `json:"kind"`
	APIVersion   string  `json:"apiVersion"`
	Namespace    string  `json:"namespace"`
	Name         string  `json:"name"`
	Replacement  string  `json:"replacement"`
	RemovedIn    *string `json:"removedIn"`
	DeprecatedIn *string `json:"deprecatedIn"`
	Severity     string  `json:"severity"`
	Source       string  `json:"source"`
	Scheduled    bool    `json:"scheduled"`
	Origin       string  `json:"origin"`
}

type skippedItemJSON struct {
	Group  string `json:"group"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type scanClusterJSON struct {
	Context string `json:"context"`
	Server  string `json:"server"`
}

type scanDoc struct {
	SchemaVersion int               `json:"schemaVersion"`
	Target        string            `json:"target"`
	Cluster       scanClusterJSON   `json:"cluster"`
	EKSCluster    string            `json:"eksCluster,omitempty"`
	Findings      []findingJSON     `json:"findings"`
	Skipped       []skippedItemJSON `json:"skipped"`
	Unverified    int               `json:"unverified"`
}

// ScanJSON writes the scan document (schemaVersion 1). findings and skipped
// are always arrays; cluster and unverified are always present.
func ScanJSON(w io.Writer, r apiscan.Result) error {
	doc := scanDoc{SchemaVersion: schemaVersion, Target: r.Target.String(), Findings: []findingJSON{}, Skipped: []skippedItemJSON{},
		Cluster: scanClusterJSON{Context: r.Cluster.Context, Server: r.Cluster.Server}, EKSCluster: r.Cluster.EKSCluster, Unverified: r.Unverified}
	for _, it := range r.Findings {
		rule := it.Finding.Rule
		doc.Findings = append(doc.Findings, findingJSON{
			Kind: it.Object.Kind, APIVersion: it.Object.APIVersion, Namespace: it.Object.Namespace, Name: it.Object.Name,
			Replacement: rule.Replacement, RemovedIn: versionPtr(rule.RemovedIn), DeprecatedIn: versionPtr(rule.DeprecatedIn),
			Severity: string(it.Finding.Severity), Source: rule.Source, Scheduled: rule.Scheduled(), Origin: it.Object.Origin,
		})
	}
	for _, s := range r.Skipped {
		doc.Skipped = append(doc.Skipped, skippedItemJSON{Group: s.Group, Kind: s.Kind, Reason: s.Reason})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
