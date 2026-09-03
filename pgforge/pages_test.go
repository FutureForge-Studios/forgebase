package main

import (
	"strings"
	"testing"
)

// renderContent throws away the Execute error, so a broken template silently
// renders a truncated page instead of failing. This renders the Database page
// body with the exact key set its handler passes and asserts the connection
// strings actually came out, which is the part a typo in a field name would
// quietly drop.
func TestDatabaseBodyRendersConnectionStrings(t *testing.T) {
	data := map[string]any{
		"DirectURL":  "postgresql://demo:pw@example.test:5432/demo?sslmode=require",
		"PooledURL":  "postgresql://demo:pw@example.test:6543/demo",
		"ReplicaURL": "postgresql://demo:pw@example.test:5434/demo?sslmode=require",
		// empty slices, not nil: the handler always passes real slices, and the
		// template calls len on them
		"Slug":       "demo", "Exts": []any{}, "Size": "1 MB", "Conns": 1,
		"StmtTimeout": "", "IdleTimeout": "", "Pubs": []any{}, "FDW": []any{},
		"ReplicaOn": true, "NInstalled": 0, "NAvail": 0,
		"MaxConns": 200, "ConnLimit": 20, "Version": "17.10",
		"Roles": []any{}, "Domain": "example.test", "CanAdmin": true,
		"CanCreateDB": true,
	}
	out := string(renderContent(databaseBody, data))
	for _, want := range []string{
		data["DirectURL"].(string),
		data["PooledURL"].(string),
		data["ReplicaURL"].(string),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Database page did not render %q", want)
		}
	}
	// A truncated render is the failure mode this test exists for: the body ends
	// with the extension filter script, so its absence means Execute bailed
	// midway and renderContent swallowed the error.
	if !strings.Contains(out, "function filterExt()") {
		t.Error("Database page render stopped early - template error swallowed by renderContent")
	}
	// The copy buttons are inert without cp(), which rides along on the body.
	if !strings.Contains(out, "function cp(") {
		t.Error("copyJS missing - the copy buttons would do nothing")
	}
}
