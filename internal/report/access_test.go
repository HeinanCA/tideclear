package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
)

const wantMissingText = `
missing permissions (the scan could not read these, so its results are partial):
  list nodes
  list deployments.apps
  list statefulsets.apps
  list ingresses.networking.k8s.io

suggested ClusterRole:
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: tideclear-reader
rules:
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["list"]
- apiGroups: ["apps"]
  resources: ["deployments", "statefulsets"]
  verbs: ["list"]
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses"]
  verbs: ["list"]
`

var missingPerms = []apiscan.Permission{
	{Group: "", Resource: "nodes", Verb: "list"},
	{Group: "apps", Resource: "deployments", Verb: "list"},
	{Group: "apps", Resource: "statefulsets", Verb: "list"},
	{Group: "networking.k8s.io", Resource: "ingresses", Verb: "list"},
}

func TestMissingPermissionsText(t *testing.T) {
	var with, without bytes.Buffer
	if err := ScanText(&with, apiscan.Result{}, Extras{Missing: missingPerms}); err != nil {
		t.Fatal(err)
	}
	if err := ScanText(&without, apiscan.Result{}, Extras{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(with.String(), without.String()); got != wantMissingText {
		t.Errorf("section =\n%s\nwant\n%s", got, wantMissingText)
	}
	if strings.Contains(without.String(), "missing permissions") {
		t.Errorf("section printed with nothing missing:\n%s", without.String())
	}
}

func TestMissingPermissionsJSON(t *testing.T) {
	var with, without bytes.Buffer
	if err := ScanJSON(&with, apiscan.Result{}, Extras{Missing: missingPerms[:2]}); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&without, apiscan.Result{}, Extras{}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Missing []map[string]string `json:"missingPermissions"`
	}
	if err := json.Unmarshal(with.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{{"group": "", "resource": "nodes", "verb": "list"}, {"group": "apps", "resource": "deployments", "verb": "list"}}
	if len(doc.Missing) != 2 || doc.Missing[0]["resource"] != "nodes" || doc.Missing[1]["group"] != "apps" || doc.Missing[1]["verb"] != want[1]["verb"] {
		t.Errorf("missingPermissions = %v, want %v", doc.Missing, want)
	}
	if strings.Contains(without.String(), "missingPermissions") {
		t.Errorf("field present with nothing missing:\n%s", without.String())
	}
}
