package report

import (
	"time"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/reconcile"
)

// CheckSkipped names a check that did not run and why.
type CheckSkipped struct{ Check, Reason string }

// Extras is what the optional checks add to a scan report. The zero value
// adds nothing, so a plain scan prints exactly what it always did.
type Extras struct {
	Insights    []reconcile.Row
	Addons      []addons.Row
	Nodes       []nodes.Row // findings only; the section is omitted when empty
	AddonsRan   bool        // the add-on check ran, so the section shows even when empty
	InsightsRan bool        // --insights was given, so the section shows even when empty
	Skipped     []CheckSkipped
}

// unknowns counts the rows that leave the result unproven.
func (e Extras) unknowns() int {
	n := 0
	for _, r := range e.Insights {
		if r.Verdict == reconcile.VerdictUnknown {
			n++
		}
	}
	for _, r := range e.Addons {
		if r.Status == addons.StatusUnknown {
			n++
		}
	}
	return n
}

// extrasJSON is embedded in scanDoc. A pointer to a slice keeps an empty
// insights or add-ons array visible (present whenever that check ran) while a nil one is omitted.
type extrasJSON struct {
	Insights      *[]insightRowJSON  `json:"insights,omitempty"`
	Addons        *[]addonRowJSON    `json:"addons,omitempty"`
	Nodes         []nodeRowJSON      `json:"nodes,omitempty"`
	ChecksSkipped []checkSkippedJSON `json:"checksSkipped,omitempty"`
}

type checkSkippedJSON struct {
	Check  string `json:"check"`
	Reason string `json:"reason"`
}

type insightRowJSON struct {
	Usage       string       `json:"usage"`
	APIVersion  string       `json:"apiVersion"`
	Kind        string       `json:"kind"`
	Resource    string       `json:"resource"`
	AWSStatus   string       `json:"awsStatus"`
	Verdict     string       `json:"verdict"`
	ClearsOn    *string      `json:"clearsOn"`
	Reason      string       `json:"reason"`
	LastRefresh *string      `json:"lastRefreshTime"`
	Objects     []objectJSON `json:"objects"`
	Clients     []clientJSON `json:"clients"`
}

type objectJSON struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Origin     string `json:"origin"`
}

type clientJSON struct {
	UserAgent       string  `json:"userAgent"`
	Requests        int32   `json:"requests"`
	LastRequestTime *string `json:"lastRequestTime"`
}

func (e Extras) json() extrasJSON {
	var out extrasJSON
	if e.InsightsRan {
		rows := make([]insightRowJSON, 0, len(e.Insights))
		for _, r := range e.Insights {
			rows = append(rows, rowJSON(r))
		}
		out.Insights = &rows
	}
	if e.AddonsRan || len(e.Addons) > 0 {
		rows := make([]addonRowJSON, 0, len(e.Addons))
		for _, r := range e.Addons {
			rows = append(rows, addonJSON(r))
		}
		out.Addons = &rows
	}
	for _, r := range e.Nodes {
		out.Nodes = append(out.Nodes, nodeJSON(r))
	}
	for _, s := range e.Skipped {
		out.ChecksSkipped = append(out.ChecksSkipped, checkSkippedJSON(s))
	}
	return out
}

func rowJSON(r reconcile.Row) insightRowJSON {
	j := insightRowJSON{
		Usage: r.Usage, APIVersion: r.APIVersion, Kind: r.Kind, Resource: r.Resource, AWSStatus: r.AWSStatus,
		Verdict: string(r.Verdict), ClearsOn: timePtr(r.ClearsOn), Reason: r.Reason,
		Objects: []objectJSON{}, Clients: []clientJSON{},
	}
	if !r.LastRefresh.IsZero() {
		j.LastRefresh = timePtr(&r.LastRefresh)
	}
	for _, o := range r.Objects {
		j.Objects = append(j.Objects, objectJSON{APIVersion: o.APIVersion, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Origin: o.Origin})
	}
	for _, c := range r.Clients {
		j.Clients = append(j.Clients, clientJSON{UserAgent: c.UserAgent, Requests: c.Requests, LastRequestTime: timePtr(c.LastRequest)})
	}
	return j
}

func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
