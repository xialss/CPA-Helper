package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseOpenAISummaryNormalizesCurrentStatus(t *testing.T) {
	status, err := parseOpenAISummary(openAITestSummary())
	if err != nil {
		t.Fatal(err)
	}
	if status.OverallStatus != "operational" || len(status.Services) != 1 || status.Groups == nil || status.Services[0].Samples == nil || status.Incidents == nil {
		t.Fatalf("unexpected OpenAI summary result: %#v", status)
	}

	base := `{"page":{"id":"openai","name":"OpenAI","updated_at":"2026-07-29T10:00:00Z"},"status":{"indicator":"none"},"components":[]`
	status, err = parseOpenAISummary([]byte(base + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if status.Incidents == nil || len(status.Incidents) != 0 {
		t.Fatalf("omitted incidents = %#v, want empty array", status.Incidents)
	}
	for name, suffix := range map[string]string{
		"null":       `,"incidents":null}`,
		"wrong type": `,"incidents":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOpenAISummary([]byte(base + suffix)); err == nil {
				t.Fatalf("OpenAI incidents %s should fail", name)
			}
		})
	}
	unsafeLink := []byte(`{"page":{"id":"openai","name":"OpenAI","updated_at":"2026-07-29T10:00:00Z"},"status":{"indicator":"none"},"components":[],"incidents":[{"id":"incident","name":"Incident","status":"investigating","shortlink":"javascript:alert(1)"}]}`)
	if _, err := parseOpenAISummary(unsafeLink); err == nil {
		t.Fatal("unsafe incident shortlink should fail")
	}
}

func TestParseOpenAIPageHistoryBuildsOfficialGroupsWithIncidentTitles(t *testing.T) {
	summary, now, services := loadOpenAIHistoryTestData(t)
	current := ModelMonitorSourceStatus{Services: services}
	if err := applyOpenAIHistory(&current, summary, now); err != nil {
		t.Fatal(err)
	}
	if current.Groups == nil || current.Services == nil || len(current.Services) != 0 || len(current.Groups) != 5 {
		t.Fatalf("unexpected OpenAI grouped shape: %#v", current)
	}
	if current.Groups[0].Name != "APIs" || current.Groups[1].Name != "ChatGPT" || current.Groups[2].Name != "Codex" || current.Groups[3].Name != "FedRAMP" || current.Groups[4].Name != "Ads Platform" {
		t.Fatalf("group order = %#v", current.Groups)
	}
	apiGroup := current.Groups[0]
	if apiGroup.Status != "partial_outage" || apiGroup.UptimePercent == nil || *apiGroup.UptimePercent != 99.5 || len(apiGroup.Services) != 2 || len(apiGroup.Samples) != 90 || len(apiGroup.Services[0].Samples) != 90 {
		t.Fatalf("unexpected APIs group: %#v", apiGroup)
	}
	affectedDay := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	groupSample := findModelMonitorSample(t, apiGroup.Samples, affectedDay)
	wantGroupTitles := []string{"Elevated API error rates", "Search requests unavailable"}
	if !equalStrings(groupSample.RelatedIncidents, wantGroupTitles) {
		t.Fatalf("group related incidents = %#v, want %#v", groupSample.RelatedIncidents, wantGroupTitles)
	}
	apiSample := findModelMonitorSample(t, apiGroup.Services[0].Samples, affectedDay)
	if !equalStrings(apiSample.RelatedIncidents, []string{"Elevated API error rates"}) {
		t.Fatalf("component related incidents = %#v", apiSample.RelatedIncidents)
	}
	for _, title := range append(append([]string{}, groupSample.RelatedIncidents...), apiSample.RelatedIncidents...) {
		if strings.HasPrefix(title, "incident-") {
			t.Fatalf("raw incident ID leaked to response: %q", title)
		}
	}
	if current.Groups[2].Status != "maintenance" || len(current.Groups[2].Samples) != 2 {
		t.Fatalf("Codex group status/history = %#v", current.Groups[2])
	}
	if current.Groups[3].Status != "major_outage" {
		t.Fatalf("FedRAMP group should use its most severe component: %#v", current.Groups[3])
	}
	adsGroup := current.Groups[4]
	if adsGroup.UptimePercent != nil || adsGroup.Samples == nil || len(adsGroup.Samples) != 0 {
		t.Fatalf("non-aggregated group should not fabricate history: %#v", adsGroup)
	}
}

func TestOpenAIIncidentLinkValidation(t *testing.T) {
	historyBody := string(readModelMonitorFixture(t, "openai_history.html"))
	incidentLinks := `,\"incident_links\":[{\"id\":\"incident-1\",\"name\":\"Elevated API error rates\"},{\"id\":\"incident-2\",\"name\":\"Search requests unavailable\"}]`
	for name, replacement := range map[string]string{
		"missing": "",
		"null":    `,\"incident_links\":null`,
	} {
		t.Run("parse "+name, func(t *testing.T) {
			body := strings.Replace(historyBody, incidentLinks, replacement, 1)
			if _, _, err := parseOpenAIPageHistory([]byte(body)); err == nil {
				t.Fatalf("incident_links %s should fail parsing", name)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*openAIPageSummary)
	}{
		{name: "duplicate ID", mutate: func(summary *openAIPageSummary) {
			summary.IncidentLinks = append(summary.IncidentLinks, summary.IncidentLinks[0])
		}},
		{name: "blank ID", mutate: func(summary *openAIPageSummary) {
			summary.IncidentLinks[0].ID = ""
		}},
		{name: "blank title", mutate: func(summary *openAIPageSummary) {
			summary.IncidentLinks[0].Name = " "
		}},
		{name: "unmapped impact", mutate: func(summary *openAIPageSummary) {
			summary.ComponentImpacts[0].IncidentID = "missing-incident"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, now, services := loadOpenAIHistoryTestData(t)
			test.mutate(&summary)
			current := ModelMonitorSourceStatus{Services: services}
			if err := applyOpenAIHistory(&current, summary, now); err == nil {
				t.Fatal("invalid incident mapping should fail")
			}
		})
	}
}

func TestOpenAIEmptyIncidentLinksAreValidWithoutRelatedImpacts(t *testing.T) {
	summary, now, services := loadOpenAIHistoryTestData(t)
	summary.IncidentLinks = []openAIIncidentLink{}
	for index := range summary.ComponentImpacts {
		summary.ComponentImpacts[index].IncidentID = ""
	}
	current := ModelMonitorSourceStatus{Services: services}
	if err := applyOpenAIHistory(&current, summary, now); err != nil {
		t.Fatal(err)
	}
	for _, group := range current.Groups {
		for _, sample := range group.Samples {
			if sample.RelatedIncidents == nil || len(sample.RelatedIncidents) != 0 {
				t.Fatalf("group sample related incidents = %#v, want []", sample.RelatedIncidents)
			}
		}
		for _, service := range group.Services {
			for _, sample := range service.Samples {
				if sample.RelatedIncidents == nil || len(sample.RelatedIncidents) != 0 {
					t.Fatalf("service sample related incidents = %#v, want []", sample.RelatedIncidents)
				}
			}
		}
	}
}

func TestApplyOpenAIHistoryRejectsInconsistentIDs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*openAIPageSummary, *[]ModelMonitorServiceStatus)
	}{
		{name: "missing current component", mutate: func(_ *openAIPageSummary, services *[]ModelMonitorServiceStatus) {
			*services = (*services)[1:]
		}},
		{name: "missing group uptime", mutate: func(summary *openAIPageSummary, _ *[]ModelMonitorServiceStatus) {
			summary.ComponentUptimes = summary.ComponentUptimes[:len(summary.ComponentUptimes)-1]
		}},
		{name: "duplicate component member", mutate: func(summary *openAIPageSummary, _ *[]ModelMonitorServiceStatus) {
			summary.Structure.Items[1].Group.Components = append(summary.Structure.Items[1].Group.Components, summary.Structure.Items[0].Group.Components[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, now, services := loadOpenAIHistoryTestData(t)
			test.mutate(&summary, &services)
			current := ModelMonitorSourceStatus{Services: services}
			if err := applyOpenAIHistory(&current, summary, now); err == nil {
				t.Fatal("inconsistent OpenAI history should fail")
			}
		})
	}
	if _, err := parseOpenAIComponents([]byte(`{"components":[{"id":"duplicate","name":"First","status":"operational"},{"id":"duplicate","name":"Second","status":"operational"}]}`)); err == nil {
		t.Fatal("duplicate current component should fail")
	}
}

func TestParseOpenAIUptimeRejectsNonFiniteValues(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "+Inf", "-Inf", "Infinity", "+Infinity", "-Infinity"} {
		t.Run(value, func(t *testing.T) {
			item := openAIPageUptime{
				Uptime:             value,
				DataAvailableSince: "2026-07-29T00:00:00Z",
			}
			if _, err := parseOpenAIUptime(item, "test component"); err == nil {
				t.Fatalf("non-finite uptime %q should fail", value)
			}
		})
	}
}

func TestModelMonitorAggregationIsolatesOpenAINonFiniteUptime(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	badOpenAIHistory := strings.Replace(
		string(readModelMonitorFixture(t, "openai_history.html")),
		`\"uptime\":\"99.90\"`,
		`\"uptime\":\"NaN\"`,
		1,
	)
	if !strings.Contains(badOpenAIHistory, `\"uptime\":\"NaN\"`) {
		t.Fatal("failed to inject non-finite OpenAI uptime into fixture")
	}
	openAIClient := openAITestClientWithHistory(t, openAITestSummary(), []byte(badOpenAIHistory))
	anthropicClient := anthropicTestClient(t, readModelMonitorFixture(t, "anthropic_summary.json"), readModelMonitorFixture(t, "anthropic_history.html"))
	fakeClient := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "status.input.im":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(readModelMonitorFixture(t, "input_im_status.json")))), Request: request}, nil
		case "status.openai.com":
			return openAIClient.Transport.RoundTrip(request)
		case "status.claude.com":
			return anthropicClient.Transport.RoundTrip(request)
		default:
			return nil, fmt.Errorf("unexpected model monitor host %q", request.URL.Host)
		}
	})}
	app.modelMonitorHTTPClient = func(ModelMonitorProxyConfig) (*http.Client, error) { return fakeClient, nil }

	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/model-monitor", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !json.Valid(recorder.Body.Bytes()) {
		t.Fatalf("aggregate response status/JSON = %d/%q, want 200 and valid JSON", recorder.Code, recorder.Body.String())
	}
	var response ModelMonitorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sources) != 3 || response.Sources[0].CollectionState != modelMonitorCollectionOK || response.Sources[1].CollectionState != modelMonitorCollectionError || response.Sources[2].CollectionState != modelMonitorCollectionOK {
		t.Fatalf("non-finite OpenAI uptime was not isolated: %#v", response.Sources)
	}
}

func TestCollectOpenAIStatusRejectsUnknownSummaryComponent(t *testing.T) {
	currentBody := []byte(`{"page":{"id":"openai","name":"OpenAI","updated_at":"2026-07-29T10:00:00Z"},"status":{"indicator":"none"},"components":[{"id":"summary-only","name":"Unknown","status":"operational"}],"incidents":[]}`)
	client := openAITestClient(t, currentBody)
	if _, err := collectOpenAIStatus(context.Background(), client, "https://status.openai.com"); err == nil {
		t.Fatal("summary component missing from components response should fail")
	}
}

func TestParseInputIMStatusSortsMinuteSamplesAndKeepsUptime(t *testing.T) {
	status, err := parseInputIMStatus(readModelMonitorFixture(t, "input_im_status.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := status.Services[0]
	if status.OverallStatus != "major_outage" || status.Groups == nil || service.UptimePercent == nil || *service.UptimePercent != 98.5 || len(service.Samples) != 2 {
		t.Fatalf("unexpected AI.INPUT.IM status: %#v", status)
	}
	if !service.Samples[0].Timestamp.Before(service.Samples[1].Timestamp) || service.LastError == nil {
		t.Fatalf("samples not sorted or last error missing: %#v", service)
	}
	if _, err := parseInputIMStatus([]byte(`{"all_ok":true,"generated_at":1,"services":[]}`)); err == nil {
		t.Fatal("empty AI.INPUT.IM services should fail")
	}

	ok := true
	uptime := 100.0
	history := make([]inputIMSample, 65)
	for index := range history {
		history[index] = inputIMSample{Timestamp: int64(index + 1), OK: &ok}
	}
	body, err := json.Marshal(inputIMPayload{
		AllOK:       &ok,
		GeneratedAt: 65,
		Services: []inputIMService{{
			Model: "gpt-window", UptimePct: &uptime,
			Last: &inputIMSample{Timestamp: 65, OK: &ok}, History: history,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	window, err := parseInputIMStatus(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Services[0].Samples) != 60 || window.Services[0].Samples[0].Timestamp.Unix() != 6 {
		t.Fatalf("minute window = %#v, want latest 60 samples", window.Services[0].Samples)
	}
}

func TestParseInputIMStatusRetainsSameSecondSamplesInStableInputOrder(t *testing.T) {
	ok := true
	failed := false
	uptime := 99.5
	firstLatency := 240.0
	firstError := "first failure"
	secondLatency := 80.0
	secondError := "second detail"
	laterLatency := 15.0
	body, err := json.Marshal(inputIMPayload{
		AllOK:       &ok,
		GeneratedAt: 300,
		Services: []inputIMService{{
			Model: "gpt-same-second", UptimePct: &uptime,
			Last: &inputIMSample{Timestamp: 300, OK: &ok, LatencyMS: &laterLatency},
			History: []inputIMSample{
				{Timestamp: 300, OK: &ok, LatencyMS: &laterLatency},
				{Timestamp: 200, OK: &failed, LatencyMS: &firstLatency, Error: &firstError},
				{Timestamp: 200, OK: &ok, LatencyMS: &secondLatency, Error: &secondError},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	status, err := parseInputIMStatus(body)
	if err != nil {
		t.Fatal(err)
	}
	samples := status.Services[0].Samples
	if len(samples) != 3 || samples[0].Timestamp.Unix() != 200 || samples[1].Timestamp.Unix() != 200 || samples[2].Timestamp.Unix() != 300 {
		t.Fatalf("same-second samples = %#v, want stable ascending order", samples)
	}
	if samples[0].Status != "major_outage" || samples[0].LatencyMS == nil || *samples[0].LatencyMS != firstLatency || samples[0].Error == nil || *samples[0].Error != firstError {
		t.Fatalf("first same-second sample = %#v, want first input sample", samples[0])
	}
	if samples[1].Status != "operational" || samples[1].LatencyMS == nil || *samples[1].LatencyMS != secondLatency || samples[1].Error == nil || *samples[1].Error != secondError {
		t.Fatalf("second same-second sample = %#v, want second input sample", samples[1])
	}
}

func TestParseInputIMStatusRejectsUnsafeTimestamps(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*inputIMPayload)
	}{
		{name: "millisecond generated_at", mutate: func(payload *inputIMPayload) {
			payload.GeneratedAt = 1785326210000
		}},
		{name: "out-of-range last timestamp", mutate: func(payload *inputIMPayload) {
			payload.Services[0].Last.Timestamp = 253402300800
		}},
		{name: "out-of-range history timestamp", mutate: func(payload *inputIMPayload) {
			payload.Services[0].History[0].Timestamp = 9223372036854775807
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload inputIMPayload
			if err := json.Unmarshal(readModelMonitorFixture(t, "input_im_status.json"), &payload); err != nil {
				t.Fatal(err)
			}
			test.mutate(&payload)
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseInputIMStatus(body); err == nil {
				t.Fatal("unsafe AI.INPUT.IM timestamp should fail")
			}
		})
	}
}

func TestParseInputIMTimestampBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		valid bool
	}{
		{name: "first positive Unix second", value: 1, valid: true},
		{name: "last RFC3339 JSON second", value: 253402300799, valid: true},
		{name: "zero", value: 0, valid: false},
		{name: "negative", value: -1, valid: false},
		{name: "first second after RFC3339 JSON range", value: 253402300800, valid: false},
		{name: "maximum int64", value: math.MaxInt64, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			timestamp, valid := parseInputIMTimestamp(test.value)
			if valid != test.valid {
				t.Fatalf("parseInputIMTimestamp(%d) valid = %t, want %t", test.value, valid, test.valid)
			}
			if valid && timestamp.Unix() != test.value {
				t.Fatalf("parseInputIMTimestamp(%d) normalized to %d", test.value, timestamp.Unix())
			}
		})
	}
}

func TestModelMonitorAggregationIsolatesInputIMUnsafeTimestamp(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	badInputIM := strings.Replace(
		string(readModelMonitorFixture(t, "input_im_status.json")),
		`"generated_at":1785326215`,
		`"generated_at":1785326210000`,
		1,
	)
	if !strings.Contains(badInputIM, `"generated_at":1785326210000`) {
		t.Fatal("failed to inject millisecond AI.INPUT.IM timestamp into fixture")
	}
	openAIClient := openAITestClient(t, openAITestSummary())
	anthropicClient := anthropicTestClient(t, readModelMonitorFixture(t, "anthropic_summary.json"), readModelMonitorFixture(t, "anthropic_history.html"))
	fakeClient := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "status.input.im":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(badInputIM)), Request: request}, nil
		case "status.openai.com":
			return openAIClient.Transport.RoundTrip(request)
		case "status.claude.com":
			return anthropicClient.Transport.RoundTrip(request)
		default:
			return nil, fmt.Errorf("unexpected model monitor host %q", request.URL.Host)
		}
	})}
	app.modelMonitorHTTPClient = func(ModelMonitorProxyConfig) (*http.Client, error) { return fakeClient, nil }

	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/model-monitor", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !json.Valid(recorder.Body.Bytes()) {
		t.Fatalf("aggregate response status/JSON = %d/%q, want 200 and valid JSON", recorder.Code, recorder.Body.String())
	}
	var response ModelMonitorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sources) != 3 || response.Sources[0].ID != "ai-input-im" || response.Sources[0].CollectionState != modelMonitorCollectionError || response.Sources[1].ID != "openai" || response.Sources[1].CollectionState != modelMonitorCollectionOK || response.Sources[2].ID != "anthropic" || response.Sources[2].CollectionState != modelMonitorCollectionOK {
		t.Fatalf("unsafe AI.INPUT.IM timestamp was not isolated: %#v", response.Sources)
	}
	if response.Sources[0].CollectionError == nil || response.Sources[1].CollectionError != nil || response.Sources[2].CollectionError != nil {
		t.Fatalf("aggregate source errors = %#v, want only AI.INPUT.IM error", response.Sources)
	}
}

func TestParseAnthropicStatusAndHistory(t *testing.T) {
	current, err := parseAnthropicSummary(readModelMonitorFixture(t, "anthropic_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if current.OverallStatus != "degraded_performance" || len(current.Services) != 2 || len(current.Incidents) != 1 || current.Groups == nil {
		t.Fatalf("unexpected Anthropic summary: %#v", current)
	}
	if current.Incidents[0].Name != "Elevated errors on Claude API" || current.Incidents[0].URL == nil {
		t.Fatalf("unexpected Anthropic incident: %#v", current.Incidents[0])
	}
	emptyIncidents, err := parseAnthropicSummary(mutateAnthropicSummary(t, func(payload map[string]any) {
		payload["incidents"] = []any{}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if emptyIncidents.Incidents == nil || len(emptyIncidents.Incidents) != 0 {
		t.Fatalf("empty Anthropic incidents = %#v, want []", emptyIncidents.Incidents)
	}

	services, err := parseAnthropicPageHistory(readModelMonitorFixture(t, "anthropic_history.html"), current.Services)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0].ID != "claude-api" || services[0].UptimePercent != nil || len(services[0].Samples) != 3 {
		t.Fatalf("unexpected Anthropic services: %#v", services)
	}
	if services[0].Samples[0].Status != "operational" || services[0].Samples[1].Status != "partial_outage" || services[0].Samples[2].Status != "major_outage" {
		t.Fatalf("unexpected Anthropic outage mapping: %#v", services[0].Samples)
	}
	wantTitles := []string{"Elevated API errors", "Slow API responses"}
	if !equalStrings(services[0].Samples[1].RelatedIncidents, wantTitles) {
		t.Fatalf("related incidents = %#v, want %#v", services[0].Samples[1].RelatedIncidents, wantTitles)
	}
	for _, title := range services[0].Samples[1].RelatedIncidents {
		if strings.HasPrefix(title, "event-") {
			t.Fatalf("raw Anthropic event code leaked: %q", title)
		}
	}
	for _, service := range services {
		if service.Samples == nil {
			t.Fatalf("Anthropic samples must be non-nil: %#v", service)
		}
		for _, sample := range service.Samples {
			if sample.RelatedIncidents == nil {
				t.Fatalf("Anthropic related incidents must be non-nil: %#v", sample)
			}
		}
	}
}

func TestParseAnthropicHistoryDeduplicatesTrimmedDisplayTitlesAcrossCodes(t *testing.T) {
	current, err := parseAnthropicSummary(readModelMonitorFixture(t, "anthropic_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(
		string(readModelMonitorFixture(t, "anthropic_history.html")),
		`{"name":"Slow API responses","code":"event-slow"}`,
		`{"name":"  Elevated API errors  ","code":"event-same-title"},{"name":"Slow API responses","code":"event-slow"}`,
		1,
	)
	services, err := parseAnthropicPageHistory([]byte(body), current.Services)
	if err != nil {
		t.Fatal(err)
	}
	wantTitles := []string{"Elevated API errors", "Slow API responses"}
	if !equalStrings(services[0].Samples[1].RelatedIncidents, wantTitles) {
		t.Fatalf("related incidents = %#v, want trimmed unique titles %#v", services[0].Samples[1].RelatedIncidents, wantTitles)
	}
}

func TestParseAnthropicHistoryKeepsLatest90Days(t *testing.T) {
	days := make([]map[string]any, 95)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := range days {
		days[index] = map[string]any{
			"date": start.AddDate(0, 0, index).Format("2006-01-02"), "outages": map[string]any{}, "related_events": []any{},
		}
	}
	payload := map[string]any{
		"component": map[string]any{"code": "component", "name": "Component"},
		"days":      days,
	}
	encoded, err := json.Marshal(map[string]any{"component": payload})
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(`<script>window.uptimeData=`), encoded...)
	body = append(body, []byte(`;</script>`)...)
	services, err := parseAnthropicPageHistory(body, []ModelMonitorServiceStatus{{ID: "component", Name: "Component", Status: "operational", Samples: []ModelMonitorSample{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(services[0].Samples) != 90 || !services[0].Samples[0].Timestamp.Equal(start.AddDate(0, 0, 5)) {
		t.Fatalf("Anthropic window = %#v, want latest 90 days", services[0].Samples)
	}
}

func TestParseAnthropicHistoryAllowsAssignmentWhitespace(t *testing.T) {
	body := []byte(`<script>window
 .	 uptimeData
 = {"component":{"component":{"code":"component","name":"Component"},"days":[{"date":"2026-07-30","outages":{},"related_events":[]}]}};</script>`)
	services, err := parseAnthropicPageHistory(body, []ModelMonitorServiceStatus{{ID: "component", Name: "Component", Status: "operational", Samples: []ModelMonitorSample{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(services[0].Samples) != 1 || services[0].Samples[0].Timestamp.Format("2006-01-02") != "2026-07-30" {
		t.Fatalf("Anthropic samples = %#v, want one sample for 2026-07-30", services[0].Samples)
	}
}

func TestParseAnthropicHistoryRejectsDuplicateTopLevelComponentKey(t *testing.T) {
	current := []ModelMonitorServiceStatus{{ID: "component", Name: "Component", Status: "operational", Samples: []ModelMonitorSample{}}}
	normalBody := []byte(`<script>window.uptimeData={"component":{"component":{"code":"component","name":"Component"},"days":[{"date":"2026-07-30","outages":{},"related_events":[]}]}};</script>`)
	services, err := parseAnthropicPageHistory(normalBody, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || len(services[0].Samples) != 1 {
		t.Fatalf("normal Anthropic history = %#v, want one component and sample", services)
	}

	duplicateBody := []byte(`<script>window.uptimeData={"component":{"component":{"code":"component","name":"Component"},"days":[{"date":"2026-07-29","outages":{},"related_events":[]}]},"component":{"component":{"code":"component","name":"Component"},"days":[{"date":"2026-07-30","outages":{},"related_events":[]}]}};</script>`)
	if _, err := parseAnthropicPageHistory(duplicateBody, current); err == nil || !strings.Contains(err.Error(), `组件 key "component" 重复`) {
		t.Fatalf("duplicate Anthropic component key error = %v", err)
	}
}

func TestDecodeAnthropicUptimeComponentsRejectsInvalidJSONBoundaries(t *testing.T) {
	components, err := decodeAnthropicUptimeComponents([]byte(`{"component":{"component":{"code":"component","name":"Component"},"days":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 1 {
		t.Fatalf("Anthropic uptime components = %#v, want one component", components)
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "trailing token", body: `{"component":{}} true`},
		{name: "non-object", body: `[]`},
		{name: "empty object", body: `{}`},
		{name: "damaged value", body: `{"component":`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeAnthropicUptimeComponents([]byte(test.body)); err == nil {
				t.Fatalf("invalid Anthropic uptime JSON %q should fail", test.body)
			}
		})
	}
}

func TestParseAnthropicHistoryRejectsInvalidDays(t *testing.T) {
	tests := []struct {
		name      string
		daysField string
	}{
		{name: "missing"},
		{name: "null", daysField: `,"days":null`},
		{name: "wrong shape", daysField: `,"days":{}`},
		{name: "empty", daysField: `,"days":[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`<script>window.uptimeData={"component":{"component":{"code":"component","name":"Component"}` + test.daysField + `}};</script>`)
			if _, err := parseAnthropicPageHistory(body, []ModelMonitorServiceStatus{{ID: "component", Name: "Component", Status: "operational", Samples: []ModelMonitorSample{}}}); err == nil {
				t.Fatalf("Anthropic days %s should fail", test.name)
			}
		})
	}
}

func TestParseAnthropicHistoryRejectsInvalidStructure(t *testing.T) {
	validBody := string(readModelMonitorFixture(t, "anthropic_history.html"))
	current, err := parseAnthropicSummary(readModelMonitorFixture(t, "anthropic_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	truncatedAt := strings.LastIndex(validBody, "}")
	tests := []struct {
		name     string
		body     string
		services []ModelMonitorServiceStatus
	}{
		{name: "missing assignment", body: strings.Replace(validBody, "window.uptimeData", "window.otherData", 1), services: current.Services},
		{name: "duplicate assignment", body: validBody + validBody, services: current.Services},
		{name: "truncated object", body: validBody[:truncatedAt], services: current.Services},
		{name: "invalid assignment tail", body: strings.Replace(validBody, `};`, `}unexpected;`, 1), services: current.Services},
		{name: "non-object assignment", body: `<script>window.uptimeData=[];</script>`, services: current.Services},
		{name: "empty object", body: `<script>window.uptimeData={};</script>`, services: current.Services},
		{name: "unknown history component", body: strings.Replace(validBody, `"claude-api":`, `"unknown":`, 1), services: current.Services},
		{name: "key code mismatch", body: strings.Replace(validBody, `"code":"claude-api"`, `"code":"other"`, 1), services: current.Services},
		{name: "component name mismatch", body: strings.Replace(validBody, `"name":"Claude API (api.anthropic.com)"`, `"name":"Other"`, 1), services: current.Services},
		{name: "summary component missing history", body: validBody, services: append(append([]ModelMonitorServiceStatus{}, current.Services...), ModelMonitorServiceStatus{ID: "missing", Name: "Missing", Status: "operational", Samples: []ModelMonitorSample{}})},
		{name: "invalid date", body: strings.Replace(validBody, `"2026-07-28"`, `"2026-02-30"`, 1), services: current.Services},
		{name: "duplicate date", body: strings.Replace(validBody, `"2026-07-28"`, `"2026-07-30"`, 1), services: current.Services},
		{name: "null days", body: strings.Replace(validBody, `"days":[`, `"days":null,"ignored":[`, 1), services: current.Services},
		{name: "null outages", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":null`, 1), services: current.Services},
		{name: "unknown outage", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":{"x":120}`, 1), services: current.Services},
		{name: "null outage duration", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":{"m":null}`, 1), services: current.Services},
		{name: "negative outage", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":{"m":-1}`, 1), services: current.Services},
		{name: "non-numeric outage", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":{"m":"120"}`, 1), services: current.Services},
		{name: "null events", body: strings.Replace(validBody, `"related_events":[{"name":"Major API interruption","code":"event-major"}]`, `"related_events":null`, 1), services: current.Services},
		{name: "blank event name", body: strings.Replace(validBody, `"name":"Major API interruption"`, `"name":" "`, 1), services: current.Services},
		{name: "blank event code", body: strings.Replace(validBody, `"code":"event-major"`, `"code":" "`, 1), services: current.Services},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAnthropicPageHistory([]byte(test.body), test.services); err == nil {
				t.Fatal("invalid Anthropic history should fail")
			}
		})
	}
}

func TestParseAnthropicSummaryRejectsInvalidStructure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing incidents", mutate: func(payload map[string]any) { delete(payload, "incidents") }},
		{name: "null incidents", mutate: func(payload map[string]any) { payload["incidents"] = nil }},
		{name: "wrong incidents type", mutate: func(payload map[string]any) { payload["incidents"] = map[string]any{} }},
		{name: "null components", mutate: func(payload map[string]any) { payload["components"] = nil }},
		{name: "empty components", mutate: func(payload map[string]any) { payload["components"] = []any{} }},
		{name: "duplicate component", mutate: func(payload map[string]any) {
			components := payload["components"].([]any)
			payload["components"] = append(components, components[0])
		}},
		{name: "duplicate incident", mutate: func(payload map[string]any) {
			incidents := payload["incidents"].([]any)
			payload["incidents"] = append(incidents, incidents[0])
		}},
		{name: "invalid page time", mutate: func(payload map[string]any) { payload["page"].(map[string]any)["updated_at"] = "invalid" }},
		{name: "invalid incident time", mutate: func(payload map[string]any) {
			payload["incidents"].([]any)[0].(map[string]any)["updated_at"] = "invalid"
		}},
		{name: "unsafe incident URL", mutate: func(payload map[string]any) {
			payload["incidents"].([]any)[0].(map[string]any)["shortlink"] = "javascript:alert(1)"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := mutateAnthropicSummary(t, test.mutate)
			if _, err := parseAnthropicSummary(body); err == nil {
				t.Fatal("invalid Anthropic summary should fail")
			}
		})
	}
	if _, err := parseAnthropicSummary([]byte(`{"invalid"`)); err == nil {
		t.Fatal("invalid Anthropic JSON should fail")
	}
}

func TestCollectAnthropicStatusUsesSummaryAndRootPage(t *testing.T) {
	client := anthropicTestClient(t, readModelMonitorFixture(t, "anthropic_summary.json"), readModelMonitorFixture(t, "anthropic_history.html"))
	status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 2 || len(status.Incidents) != 1 || status.Groups == nil {
		t.Fatalf("unexpected Anthropic collection: %#v", status)
	}
	invalidHistoryClient := anthropicTestClient(t, readModelMonitorFixture(t, "anthropic_summary.json"), []byte(`<html></html>`))
	if _, err := collectAnthropicStatus(context.Background(), invalidHistoryClient, "https://status.claude.com"); err == nil {
		t.Fatal("invalid Anthropic history should fail instead of returning current-only status")
	}
}

type modelMonitorRoundTripper func(*http.Request) (*http.Response, error)

func (fn modelMonitorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type modelMonitorCloseTrackingTransport struct {
	roundTripper http.RoundTripper
	closeCalls   int
}

func (transport *modelMonitorCloseTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.roundTripper.RoundTrip(request)
}

func (transport *modelMonitorCloseTrackingTransport) CloseIdleConnections() {
	transport.closeCalls++
}

func TestModelMonitorProxyNormalizationAndTransport(t *testing.T) {
	normalized, err := normalizeModelMonitorProxyURL(" sock5://127.0.0.1:1080 ")
	if err != nil {
		t.Fatalf("normalizeModelMonitorProxyURL failed: %v", err)
	}
	if normalized != "socks5://127.0.0.1:1080" {
		t.Fatalf("normalized proxy URL = %q, want socks5://127.0.0.1:1080", normalized)
	}
	for _, value := range []string{"ftp://proxy.example.com", "https:///missing-host"} {
		if _, err := normalizeModelMonitorProxyURL(value); err == nil {
			t.Fatalf("proxy URL %q should fail validation", value)
		}
	}
	if _, err := normalizeModelMonitorProxyConfig(ModelMonitorProxyConfig{Enabled: true}); err == nil {
		t.Fatal("enabled proxy without URL should fail validation")
	}

	t.Setenv("HTTP_PROXY", "http://environment-proxy.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://environment-proxy.invalid:8080")
	t.Setenv("ALL_PROXY", "socks5://environment-proxy.invalid:1080")
	t.Setenv("NO_PROXY", "status.example.com")
	directClient, err := newModelMonitorBuiltinHTTPClient(ModelMonitorProxyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	directTransport, ok := directClient.Transport.(*http.Transport)
	if !ok || directTransport.Proxy != nil {
		t.Fatalf("disabled model monitor proxy transport = %#v, want Proxy=nil", directClient.Transport)
	}

	for _, explicitProxyURL := range []string{
		"http://127.0.0.1:7890",
		"https://127.0.0.1:7890",
		normalized,
	} {
		t.Run(explicitProxyURL, func(t *testing.T) {
			proxiedClient, err := newModelMonitorBuiltinHTTPClient(ModelMonitorProxyConfig{Enabled: true, ProxyURL: explicitProxyURL})
			if err != nil {
				t.Fatal(err)
			}
			proxiedTransport, ok := proxiedClient.Transport.(*http.Transport)
			if !ok || proxiedTransport.Proxy == nil {
				t.Fatalf("enabled model monitor proxy transport = %#v, want explicit proxy", proxiedClient.Transport)
			}
			request := httptest.NewRequest(http.MethodGet, "https://status.example.com", nil)
			proxyURL, err := proxiedTransport.Proxy(request)
			if err != nil {
				t.Fatal(err)
			}
			if proxyURL == nil || proxyURL.String() != explicitProxyURL {
				t.Fatalf("transport proxy = %v, want %q", proxyURL, explicitProxyURL)
			}
		})
	}
}

func TestModelMonitorGetRejectsInvalidResponses(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
		})}
		if _, err := modelMonitorGet(context.Background(), client, "https://status.example.com/data", "application/json"); err == nil {
			t.Fatal("non-2xx response should fail")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", modelMonitorMaxBodyBytes+1))), Request: request}, nil
		})}
		if _, err := modelMonitorGet(context.Background(), client, "https://status.example.com/data", "application/json"); err == nil {
			t.Fatal("oversized response should fail")
		}
	})
	t.Run("content type", func(t *testing.T) {
		client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(`<html></html>`)), Request: request}, nil
		})}
		if _, err := modelMonitorGet(context.Background(), client, "https://status.example.com/data", "application/json"); err == nil {
			t.Fatal("invalid content type should fail")
		}
	})
	t.Run("content type prefix", func(t *testing.T) {
		client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/jsonp"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
		})}
		if _, err := modelMonitorGet(context.Background(), client, "https://status.example.com/data", "application/json"); err == nil {
			t.Fatal("content type prefix should not be accepted")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		})}
		if _, err := modelMonitorGet(ctx, client, "https://status.example.com/data", "application/json"); err == nil {
			t.Fatal("canceled request should fail")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})}
		if _, err := modelMonitorGet(context.Background(), client, "https://status.example.com/data", "application/json"); err == nil {
			t.Fatal("timed out request should fail")
		}
	})
}

func TestModelMonitorAggregationKeepsBuiltInOrderAndPartialFailures(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	openAIClient := openAITestClient(t, openAITestSummary())
	var seenHosts sync.Map
	transport := &modelMonitorCloseTrackingTransport{roundTripper: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		seenHosts.Store(request.URL.Host, true)
		if request.URL.Host == "status.input.im" {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(readModelMonitorFixture(t, "input_im_status.json")))), Request: request}, nil
		}
		if request.URL.Host == "status.claude.com" {
			return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"upstream down"}`)), Request: request}, nil
		}
		return openAIClient.Transport.RoundTrip(request)
	})}
	fakeClient := &http.Client{Transport: transport}
	factoryCalls := 0
	app.modelMonitorHTTPClient = func(proxyCfg ModelMonitorProxyConfig) (*http.Client, error) {
		factoryCalls++
		if !proxyCfg.Enabled || proxyCfg.ProxyURL != "socks5://127.0.0.1:1080" {
			t.Fatalf("model monitor proxy config = %#v", proxyCfg)
		}
		return fakeClient, nil
	}
	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/proxy", map[string]any{"enabled": true, "proxy_url": "sock5://127.0.0.1:1080"}, cookies, http.StatusOK, nil)
	var response ModelMonitorResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, cookies, http.StatusOK, &response)
	if factoryCalls != 1 {
		t.Fatalf("model monitor HTTP client factory calls = %d, want 1", factoryCalls)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("model monitor CloseIdleConnections calls = %d, want 1", transport.closeCalls)
	}
	for _, host := range []string{"status.input.im", "status.openai.com", "status.claude.com"} {
		if _, ok := seenHosts.Load(host); !ok {
			t.Fatalf("model monitor source %q did not use the configured client", host)
		}
	}
	if len(response.Sources) != 3 || response.Sources[0].ID != "ai-input-im" || response.Sources[1].ID != "openai" || response.Sources[2].ID != "anthropic" {
		t.Fatalf("unexpected source order: %#v", response.Sources)
	}
	if response.Sources[2].Name != "Claude Status" || response.Sources[2].StatusPageURL != "https://status.claude.com" {
		t.Fatalf("unexpected Claude Status identity: %#v", response.Sources[2])
	}
	if response.Sources[0].CollectionState != modelMonitorCollectionOK || response.Sources[1].CollectionState != modelMonitorCollectionOK || response.Sources[2].CollectionState != modelMonitorCollectionError {
		t.Fatalf("unexpected collection states: %#v", response.Sources)
	}
	for _, source := range response.Sources {
		if source.Groups == nil || source.Services == nil || source.Incidents == nil {
			t.Fatalf("source arrays must be non-nil: %#v", source)
		}
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources", nil, cookies, http.StatusNotFound, nil)
}

func TestModelMonitorRouteRequiresAdmin(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler := app.Routes()
	adminCookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	if _, err := app.db.Exec(`UPDATE app_settings SET litellm_proxy_enabled = 1, litellm_proxy_url = 'http://legacy-proxy.local:7890' WHERE id = 1`); err != nil {
		t.Fatalf("seed legacy LiteLLM proxy settings: %v", err)
	}
	var proxySettings ModelMonitorProxyConfig
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/proxy", nil, adminCookies, http.StatusOK, &proxySettings)
	if proxySettings.Enabled || proxySettings.ProxyURL != "" {
		t.Fatalf("default model monitor proxy settings = %#v, want disabled and independent", proxySettings)
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-prices/litellm-proxy", nil, adminCookies, http.StatusNotFound, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/proxy", map[string]any{"enabled": true}, adminCookies, http.StatusUnprocessableEntity, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/proxy", map[string]any{"enabled": false, "proxy_url": "ftp://proxy.example.com"}, adminCookies, http.StatusUnprocessableEntity, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/proxy", map[string]any{"proxy_url": " sock5://127.0.0.1:1080 "}, adminCookies, http.StatusOK, &proxySettings)
	if proxySettings.Enabled || proxySettings.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("normalized disabled proxy settings = %#v", proxySettings)
	}
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/proxy", map[string]any{"enabled": true}, adminCookies, http.StatusOK, &proxySettings)
	if !proxySettings.Enabled || proxySettings.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved model monitor proxy settings = %#v", proxySettings)
	}
	proxySettings = ModelMonitorProxyConfig{}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/proxy", nil, adminCookies, http.StatusOK, &proxySettings)
	if !proxySettings.Enabled || proxySettings.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("persisted model monitor proxy settings = %#v", proxySettings)
	}
	appCfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	appCfg.Collector.QueueName = "updated-queue"
	if err := app.saveConfig(context.Background(), appCfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	proxySettings = ModelMonitorProxyConfig{}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/proxy", nil, adminCookies, http.StatusOK, &proxySettings)
	if !proxySettings.Enabled || proxySettings.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("general config save overwrote model monitor proxy settings: %#v", proxySettings)
	}
	modelMonitorRequest(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, adminCookies, http.StatusOK, nil)
	memberCookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, http.StatusOK, nil)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, memberCookies, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/proxy", nil, memberCookies, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/proxy", map[string]any{"enabled": false}, memberCookies, http.StatusForbidden, nil)
}

func openAITestSummary() []byte {
	return []byte(`{"page":{"id":"openai","name":"OpenAI","updated_at":"2026-07-29T10:00:00Z"},"status":{"indicator":"none"},"components":[{"id":"api","name":"API","status":"operational"}],"incidents":[]}`)
}

func openAITestClient(t *testing.T, summaryBody []byte) *http.Client {
	t.Helper()
	return openAITestClientWithHistory(t, summaryBody, readModelMonitorFixture(t, "openai_history.html"))
}

func openAITestClientWithHistory(t *testing.T, summaryBody, historyBody []byte) *http.Client {
	t.Helper()
	componentsBody := readModelMonitorFixture(t, "openai_components.json")
	return &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		contentType := "application/json"
		body := summaryBody
		switch request.URL.Path {
		case "/api/v2/components.json":
			body = componentsBody
		case "/":
			contentType = "text/html"
			body = historyBody
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})}
}

func anthropicTestClient(t *testing.T, summaryBody, historyBody []byte) *http.Client {
	t.Helper()
	return &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body []byte
		var contentType string
		switch request.URL.Path {
		case "/api/v2/summary.json":
			body = summaryBody
			contentType = "application/json"
		case "/":
			body = historyBody
			contentType = "text/html"
		default:
			t.Fatalf("unexpected Anthropic request path: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})}
}

func mutateAnthropicSummary(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(readModelMonitorFixture(t, "anthropic_summary.json"), &payload); err != nil {
		t.Fatal(err)
	}
	mutate(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func loadOpenAIHistoryTestData(t *testing.T) (openAIPageSummary, time.Time, []ModelMonitorServiceStatus) {
	t.Helper()
	summary, now, err := parseOpenAIPageHistory(readModelMonitorFixture(t, "openai_history.html"))
	if err != nil {
		t.Fatal(err)
	}
	services, err := parseOpenAIComponents(readModelMonitorFixture(t, "openai_components.json"))
	if err != nil {
		t.Fatal(err)
	}
	return summary, now, services
}

func findModelMonitorSample(t *testing.T, samples []ModelMonitorSample, timestamp time.Time) ModelMonitorSample {
	t.Helper()
	for _, sample := range samples {
		if sample.Timestamp.Equal(timestamp) {
			return sample
		}
	}
	t.Fatalf("sample %s not found", timestamp)
	return ModelMonitorSample{}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func modelMonitorRequest(t *testing.T, handler http.Handler, method, target string, body any, cookies []*http.Cookie, wantStatus int, targetBody any) []*http.Cookie {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	}
	request := httptest.NewRequest(method, target, reader)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, target, recorder.Code, wantStatus, recorder.Body.String())
	}
	if targetBody != nil && recorder.Code != http.StatusNoContent {
		if err := json.Unmarshal(recorder.Body.Bytes(), targetBody); err != nil {
			t.Fatal(err)
		}
	}
	return append(cookies, recorder.Result().Cookies()...)
}

func readModelMonitorFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/model_monitor/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
