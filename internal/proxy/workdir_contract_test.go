package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// The work-directory contract (organization ADR-021, project ADR-0003) is a
// rule about every injected tool, not about one of them. Stated only in prose
// it gets re-decided by whoever adds the next tool — and prose drifts silently
// besides, because nothing compiles it. Both halves are pinned here.

var retiredWorkDirNames = []string{"workspace_root", "workspaceRoot", "workspace_dir", "allowed_roots"}

func TestNoToolSchemaOrDescriptionCarriesARetiredName(t *testing.T) {
	it := &InjectedTools{}
	for _, tool := range it.Definitions() {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: input schema does not marshal: %v", tool.Name, err)
		}
		for _, old := range retiredWorkDirNames {
			if strings.Contains(string(schema), old) {
				t.Errorf("tool %q declares %q; the boundary is the call's work_dir", tool.Name, old)
			}
			if strings.Contains(tool.Description, old) {
				t.Errorf("tool %q describes itself with %q; the boundary is the call's work_dir", tool.Name, old)
			}
		}
	}
}

// An optional work directory is an invitation to fall back to a server-owned
// default — and here it is also the containment boundary, so an optional one
// would be no boundary at all.
func TestWorkDirIsRequiredOnEveryInjectedTool(t *testing.T) {
	it := &InjectedTools{}
	for _, tool := range it.Definitions() {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: input schema is not valid JSON: %v", tool.Name, err)
		}
		if _, declared := schema.Properties["work_dir"]; !declared {
			t.Errorf("tool %q does not declare work_dir; it is the containment boundary", tool.Name)
			continue
		}
		found := false
		for _, r := range schema.Required {
			if r == "work_dir" {
				found = true
			}
		}
		if !found {
			t.Errorf("tool %q declares work_dir but does not require it", tool.Name)
		}
	}
}
