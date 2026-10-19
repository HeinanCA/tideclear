// Package report renders results as text tables and JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
)

const (
	schemaVersion = 1
	dateLayout    = "2006-01-02"
)

// ClustersText writes the clusters table; the SUPPORT column shows the
// support state, with extended and unsupported flagged in capitals.
func ClustersText(w io.Writer, cs []eksinfo.Cluster) error {
	if len(cs) == 0 {
		_, err := fmt.Fprintln(w, "no clusters found")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CLUSTER\tREGION\tVERSION\tSTANDARD END\tEXTENDED END\tDAYS LEFT\tSUPPORT")
	for _, c := range cs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Name, c.Region, c.Version, date(c.StandardEnd), date(c.ExtendedEnd), days(c.DaysLeft), supportLabel(c.Support))
	}
	return tw.Flush()
}

// supportLabel flags states that need attention in capitals.
func supportLabel(s eksinfo.Support) string {
	switch s {
	case eksinfo.SupportExtended:
		return "EXTENDED"
	case eksinfo.SupportUnsupported:
		return "UNSUPPORTED"
	case "":
		return string(eksinfo.SupportUnknown)
	}
	return string(s)
}

func days(n *int) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(*n)
}

func date(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(dateLayout)
}

type clusterJSON struct {
	Name               string  `json:"name"`
	Region             string  `json:"region"`
	Version            string  `json:"version"`
	StandardSupportEnd *string `json:"standardSupportEnd"`
	ExtendedSupportEnd *string `json:"extendedSupportEnd"`
	DaysLeft           *int    `json:"daysLeft"`
	Support            string  `json:"support"`
	OnExtended         bool    `json:"onExtended"` // kept for compatibility; true only when support is extended
}

type skippedJSON struct {
	Region string `json:"region"`
	Error  string `json:"error"`
}

type clustersDoc struct {
	SchemaVersion  int           `json:"schemaVersion"`
	Clusters       []clusterJSON `json:"clusters"`
	SkippedRegions []skippedJSON `json:"skippedRegions"`
}

// ClustersJSON writes the clusters document (schemaVersion 1). clusters and
// skippedRegions are always arrays.
func ClustersJSON(w io.Writer, cs []eksinfo.Cluster, skipped []eksinfo.SkippedRegion) error {
	doc := clustersDoc{SchemaVersion: schemaVersion, Clusters: []clusterJSON{}, SkippedRegions: []skippedJSON{}}
	for _, c := range cs {
		doc.Clusters = append(doc.Clusters, clusterJSON{
			Name: c.Name, Region: c.Region, Version: c.Version,
			StandardSupportEnd: datePtr(c.StandardEnd), ExtendedSupportEnd: datePtr(c.ExtendedEnd),
			DaysLeft: c.DaysLeft, Support: string(c.Support), OnExtended: c.Support == eksinfo.SupportExtended,
		})
	}
	for _, s := range skipped {
		msg := ""
		if s.Err != nil {
			msg = s.Err.Error()
		}
		doc.SkippedRegions = append(doc.SkippedRegions, skippedJSON{Region: s.Region, Error: msg})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func datePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(dateLayout)
	return &s
}
