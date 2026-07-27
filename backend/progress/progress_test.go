package progress

import (
	"context"
	"testing"
)

type captureObserver struct{ events []Event }

func (o *captureObserver) Emit(event Event) { o.events = append(o.events, event) }

func TestWithScopeMergesSiteAndAccountMetadata(t *testing.T) {
	capture := &captureObserver{}
	ctx := WithObserver(context.Background(), capture)
	ctx = WithScope(ctx, Scope{Level: "site", SiteID: 7, SiteName: "alpha", SiteIndex: 2, SiteTotal: 3})
	ctx = WithScope(ctx, Scope{Level: "account", AccountID: 11, AccountAlias: "primary", Index: 1, Total: 4})

	Start(ctx, StageRates, "拉取分组倍率…")
	if len(capture.events) != 1 {
		t.Fatalf("events = %d", len(capture.events))
	}
	event := capture.events[0]
	if event.Scope != "account" || event.SiteID != 7 || event.SiteName != "alpha" || event.AccountID != 11 || event.AccountAlias != "primary" || event.Index != 1 || event.Total != 4 || event.SiteIndex != 2 || event.SiteTotal != 3 {
		t.Fatalf("event = %#v", event)
	}
}
