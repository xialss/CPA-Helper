package app

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

func validUsageSourceKey(key string) bool {
	if len(key) != 64 || strings.ToLower(key) != key {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func usageAnalyticsTableColumns(ctx context.Context, executor usageAnalyticsExecutor, table string) (map[string]bool, error) {
	queryer, ok := executor.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return nil, fmt.Errorf("usage metadata executor cannot inspect its schema")
	}
	if table != "usage_records" && table != "usage_analytics_facts" {
		return nil, fmt.Errorf("invalid usage metadata table")
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
