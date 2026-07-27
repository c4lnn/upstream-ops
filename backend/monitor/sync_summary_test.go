package monitor

import (
	"errors"
	"testing"
)

func TestNewSyncSummaryStatuses(t *testing.T) {
	tests := []struct {
		name   string
		items  []SiteAccountSyncResult
		err    error
		status string
	}{
		{name: "success", items: []SiteAccountSyncResult{{Success: true}}, status: "success"},
		{name: "partial", items: []SiteAccountSyncResult{{Success: true}, {Success: false}}, status: "partial"},
		{name: "batch error after account success", items: []SiteAccountSyncResult{{Success: true}}, err: errors.New("announcement failed"), status: "partial"},
		{name: "failed", items: []SiteAccountSyncResult{{Success: false}}, err: errors.New("failed"), status: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := NewSyncSummary(test.items, test.err)
			if summary.Status != test.status {
				t.Fatalf("status = %q, want %q", summary.Status, test.status)
			}
		})
	}
}
