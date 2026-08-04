package app

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestFilteredUsageRecordsPageReturnsOnlySelectedSourcePage(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	base := time.Date(2026, time.July, 1, 0, 0, 0, 0, appTimeLocation)
	save := func(source string, hour int) int {
		requestID := "source-page-" + strconv.Itoa(hour)
		raw := `{"api_key":"sk-` + requestID + `","provider":"codex","model":"gpt-test","source":"` + source + `","request_id":"` + requestID + `","input_tokens":1}`
		record, created, err := app.saveUsageMessage(context.Background(), []byte(raw), modelPriceBillingIndex{})
		if err != nil || !created {
			t.Fatalf("saveUsageMessage %s created=%v err=%v", requestID, created, err)
		}
		if _, err := app.db.Exec(`UPDATE usage_records SET timestamp = ? WHERE id = ?`, dbTime(base.Add(time.Duration(hour)*time.Hour)), record.ID); err != nil {
			t.Fatalf("set timestamp for %s: %v", requestID, err)
		}
		return record.ID
	}

	selectedSource := "selected-source"
	save(selectedSource, 1)
	save("other-source", 2)
	wantPageTwoID := save(selectedSource, 3)
	save(selectedSource, 4)
	save("other-source", 5)

	sourceKey := usageSourceKey(&selectedSource)
	total, records, err := app.filteredUsageRecordsPage(context.Background(), UsageFilters{SourceKey: sourceKey}, 2, 1)
	if err != nil {
		t.Fatalf("filteredUsageRecordsPage: %v", err)
	}
	if total != 3 {
		t.Fatalf("source-filtered total = %d, want 3", total)
	}
	if len(records) != 1 || records[0].ID != wantPageTwoID {
		t.Fatalf("source-filtered page 2 = %#v, want record %d", records, wantPageTwoID)
	}
	if records[0].Source == nil || *records[0].Source != selectedSource || records[0].RawJSON == "" {
		t.Fatalf("source-filtered page record = %#v, want complete selected source record", records[0])
	}

	total, records, err = app.filteredUsageRecordsPage(context.Background(), UsageFilters{SourceKey: sourceKey}, 4, 1)
	if err != nil {
		t.Fatalf("filteredUsageRecordsPage after end: %v", err)
	}
	if total != 3 || len(records) != 0 {
		t.Fatalf("source-filtered page after end total/records = %d/%#v, want 3/empty", total, records)
	}
}
