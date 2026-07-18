package handler

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// clickHouseClient wraps a ClickHouse HTTP endpoint. Shared by any handler that
// queries the otel_traces / rum_events tables directly.
type clickHouseClient struct {
	URL string
}

// ClickHouse credentials were previously hardcoded as literal strings at every
// call site (clickhouse.go, rum_handler.go, synthetics_handler.go — 9 places
// total). Centralized here and overridable via env so a real deployment only
// has to change these in one place instead of hunting through the codebase.
// Defaults match the CLICKHOUSE_USER/CLICKHOUSE_PASSWORD the clickhouse
// container itself is provisioned with in docker-compose.yml.
var (
	clickhouseUser     = getEnvOrDefault("CLICKHOUSE_USER", "pulsetrace")
	clickhousePassword = getEnvOrDefault("CLICKHOUSE_PASSWORD", "pulsetrace_secret")
)

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var intervalToSQL = map[string]string{
	"1h":  "1 HOUR",
	"24h": "1 DAY",
	"7d":  "7 DAY",
}

var intervalToBucket = map[string]string{
	"1h":  "toStartOfMinute(Timestamp)",
	"24h": "toStartOfInterval(Timestamp, INTERVAL 15 MINUTE)",
	"7d":  "toStartOfHour(Timestamp)",
}

// resolveInterval validates a user-supplied interval string, defaulting to "1h".
func resolveInterval(raw string) (key, sqlInterval, bucketExpr string) {
	if _, ok := intervalToSQL[raw]; !ok {
		raw = "1h"
	}
	return raw, intervalToSQL[raw], intervalToBucket[raw]
}

// query posts a parameterized query to ClickHouse's HTTP interface.
// params are passed as ClickHouse HTTP query-string bind parameters (param_<name>=<value>),
// referenced in the SQL as {<name>:<Type>}, so caller-supplied values are never string-concatenated into SQL.
func (c *clickHouseClient) query(sql string, params map[string]string) (*http.Response, error) {
	reqURL := c.URL
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set("param_"+k, v)
		}
		reqURL = reqURL + "?" + q.Encode()
	}

	req, err := http.NewRequest("POST", reqURL, bytes.NewBufferString(sql))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	client := &http.Client{}
	return client.Do(req)
}

// arrayParam formats a list of strings as a ClickHouse Array(String) HTTP parameter value: ['a','b']
func arrayParam(values []string) string {
	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return "[" + strings.Join(escaped, ",") + "]"
}

// stringParam formats a single string as a ClickHouse String HTTP parameter value.
func stringParam(v string) string {
	return v
}
