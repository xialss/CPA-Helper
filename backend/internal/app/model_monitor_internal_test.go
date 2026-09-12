package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
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

func TestApplyOpenAIHistoryHandlesUndefinedImpactEndAt(t *testing.T) {
	t.Run("undefined uses page now", func(t *testing.T) {
		summary, _, services := loadOpenAIHistoryTestData(t)
		const conversationsID = "01JMXBNJXGV1T5GT2M9XA83XNG"
		for groupIndex := range summary.Structure.Items {
			for componentIndex := range summary.Structure.Items[groupIndex].Group.Components {
				component := &summary.Structure.Items[groupIndex].Group.Components[componentIndex]
				if component.ComponentID == "chat" {
					component.ComponentID = conversationsID
				}
			}
		}
		for uptimeIndex := range summary.ComponentUptimes {
			if summary.ComponentUptimes[uptimeIndex].ComponentID == "chat" {
				summary.ComponentUptimes[uptimeIndex].ComponentID = conversationsID
			}
		}
		for serviceIndex := range services {
			if services[serviceIndex].ID == "chat" {
				services[serviceIndex].ID = conversationsID
			}
		}
		undefined := "$undefined"
		summary.ComponentImpacts = append(summary.ComponentImpacts, openAIPageImpact{
			ComponentID: conversationsID,
			StartAt:     "2026-08-04T12:43:44.616Z",
			EndAt:       &undefined,
			Status:      "degraded_performance",
			IncidentID:  "01KZ6CT9K9Q52S3707W75MNBGB",
		})
		summary.IncidentLinks = append(summary.IncidentLinks, openAIIncidentLink{
			ID:   "01KZ6CT9K9Q52S3707W75MNBGB",
			Name: "Conversations degraded performance",
		})
		now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
		current := ModelMonitorSourceStatus{Services: services}
		if err := applyOpenAIHistory(&current, summary, now); err != nil {
			t.Fatal(err)
		}

		nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		groupSample := findModelMonitorSample(t, current.Groups[1].Samples, nowDay)
		if groupSample.Status != "degraded_performance" {
			t.Fatalf("ongoing impact group sample status = %q, want degraded_performance", groupSample.Status)
		}
		if !equalStrings(groupSample.RelatedIncidents, []string{"Conversations degraded performance"}) {
			t.Fatalf("ongoing impact group related incidents = %#v", groupSample.RelatedIncidents)
		}
		conversationsSample := findModelMonitorSample(t, current.Groups[1].Services[0].Samples, nowDay)
		if conversationsSample.Status != "degraded_performance" {
			t.Fatalf("ongoing impact sample status = %q, want degraded_performance", conversationsSample.Status)
		}
		if !equalStrings(conversationsSample.RelatedIncidents, []string{"Conversations degraded performance"}) {
			t.Fatalf("ongoing impact related incidents = %#v", conversationsSample.RelatedIncidents)
		}
	})

	t.Run("undefined end before start", func(t *testing.T) {
		summary, now, services := loadOpenAIHistoryTestData(t)
		undefined := "$undefined"
		summary.ComponentImpacts[0].StartAt = now.Add(time.Second).Format(time.RFC3339Nano)
		summary.ComponentImpacts[0].EndAt = &undefined
		current := ModelMonitorSourceStatus{Services: services}
		err := applyOpenAIHistory(&current, summary, now)
		if err == nil || !strings.Contains(err.Error(), "impact 时间范围无效") {
			t.Fatalf("impact end_at %q with future start_at error = %v, want impact time-range error", undefined, err)
		}
	})

	for _, test := range []struct {
		name  string
		endAt string
	}{
		{name: "invalid end time", endAt: "not-a-time"},
		{name: "end before start", endAt: "2026-07-27T11:00:00Z"},
		{name: "non-exact undefined marker", endAt: " $undefined "},
	} {
		t.Run(test.name, func(t *testing.T) {
			summary, now, services := loadOpenAIHistoryTestData(t)
			endAt := test.endAt
			summary.ComponentImpacts[0].EndAt = &endAt
			current := ModelMonitorSourceStatus{Services: services}
			err := applyOpenAIHistory(&current, summary, now)
			if err == nil || !strings.Contains(err.Error(), "impact 时间范围无效") {
				t.Fatalf("impact end_at %q error = %v, want impact time-range error", test.endAt, err)
			}
		})
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
	deepseekClient := deepseekFlashcatTestClient(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	transport := &modelMonitorCloseTrackingTransport{roundTripper: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "status.input.im":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(readModelMonitorFixture(t, "input_im_status.json")))), Request: request}, nil
		case "status.openai.com":
			return openAIClient.Transport.RoundTrip(request)
		case "status.claude.com":
			return anthropicClient.Transport.RoundTrip(request)
		case "statuspage.flashcat.cloud":
			return deepseekClient.Transport.RoundTrip(request)
		default:
			return nil, fmt.Errorf("unexpected model monitor host %q", request.URL.Host)
		}
	})}
	fakeClient := &http.Client{Transport: transport}
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
	if len(response.Sources) != 4 || response.Sources[0].CollectionState != modelMonitorCollectionOK || response.Sources[1].CollectionState != modelMonitorCollectionError || response.Sources[2].CollectionState != modelMonitorCollectionOK || response.Sources[3].ID != "deepseek" || response.Sources[3].CollectionState != modelMonitorCollectionOK {
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
	deepseekClient := deepseekFlashcatTestClient(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	fakeClient := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "status.input.im":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(badInputIM)), Request: request}, nil
		case "status.openai.com":
			return openAIClient.Transport.RoundTrip(request)
		case "status.claude.com":
			return anthropicClient.Transport.RoundTrip(request)
		case "statuspage.flashcat.cloud":
			return deepseekClient.Transport.RoundTrip(request)
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
	if len(response.Sources) != 4 || response.Sources[0].ID != "ai-input-im" || response.Sources[0].CollectionState != modelMonitorCollectionError || response.Sources[1].ID != "openai" || response.Sources[1].CollectionState != modelMonitorCollectionOK || response.Sources[2].ID != "anthropic" || response.Sources[2].CollectionState != modelMonitorCollectionOK || response.Sources[3].ID != "deepseek" || response.Sources[3].CollectionState != modelMonitorCollectionOK {
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

func TestCollectAnthropicStatusUsesLazyUptimeShowcase(t *testing.T) {
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	historyBody := readModelMonitorFixture(t, "anthropic_lazy_history.html")
	showcaseBody := readModelMonitorFixture(t, "anthropic_uptime_showcase.json")
	var requests []string
	client := &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "status.claude.com" {
			t.Fatalf("unexpected Anthropic request: %s %s", request.Method, request.URL)
		}
		requests = append(requests, request.URL.RequestURI())
		body := summaryBody
		contentType := "application/json"
		switch request.URL.Path {
		case "/api/v2/summary.json":
		case "/":
			body = historyBody
			contentType = "text/html"
		case "/uptime_showcase":
			body = showcaseBody
			if request.URL.RawQuery != "components=claude-ai%2Cclaude-api" {
				t.Fatalf("Anthropic showcase query = %q", request.URL.RawQuery)
			}
		default:
			t.Fatalf("unexpected Anthropic request path: %s", request.URL.Path)
		}
		if request.Header.Get("Accept") != contentType {
			t.Fatalf("Anthropic Accept = %q, want %q", request.Header.Get("Accept"), contentType)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})}
	status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com")
	if err != nil {
		t.Fatal(err)
	}
	wantRequests := []string{"/api/v2/summary.json", "/", "/uptime_showcase?components=claude-ai%2Cclaude-api"}
	if !equalStrings(requests, wantRequests) {
		t.Fatalf("Anthropic requests = %#v, want %#v", requests, wantRequests)
	}
	legacy, err := collectAnthropicStatus(context.Background(), anthropicTestClient(t, summaryBody, readModelMonitorFixture(t, "anthropic_history.html")), "https://status.claude.com")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status, legacy) {
		t.Fatalf("lazy Anthropic status = %#v, want legacy status %#v", status, legacy)
	}
}

func TestParseAnthropicLazyUptimeComponentIDsIgnoresNonElementMarkup(t *testing.T) {
	placeholder := `<div class="uptime-lazy-placeholder" data-uptime-lazy="not-a-component"></div>`
	for _, element := range []string{"script", "style", "title", "textarea", "xmp", "iframe", "noembed", "noframes"} {
		t.Run(element, func(t *testing.T) {
			body := `<` + element + ` data-example='` + placeholder + `'>` + `"</different-element>"` + placeholder + `</` + strings.ToUpper(element) + `>`
			ids, err := parseAnthropicLazyUptimeComponentIDs([]byte(body))
			if err != nil || len(ids) != 0 {
				t.Fatalf("non-element markup yielded IDs %#v, error %v", ids, err)
			}
		})
	}
	for _, body := range []string{
		`<!-- ` + placeholder + ` -->`,
		`<!-- ` + placeholder,
		`<p data-example='` + placeholder + `'>Example</p>`,
		`<script>const example = '</style>` + placeholder + `';</script>`,
		`<plaintext>` + placeholder,
	} {
		ids, err := parseAnthropicLazyUptimeComponentIDs([]byte(body))
		if err != nil || len(ids) != 0 {
			t.Fatalf("non-element markup %q yielded IDs %#v, error %v", body, ids, err)
		}
	}
	body := `<DIV CLASS="extra uptime-lazy-placeholder" DATA-UPTIME-LAZY="claude&#45;ai"></DIV>
<div
 data-uptime-lazy=claude-api
 class=uptime-lazy-placeholder aria-hidden=true></div>`
	ids, err := parseAnthropicLazyUptimeComponentIDs([]byte(body))
	if err != nil || !equalStrings(ids, []string{"claude-ai", "claude-api"}) {
		t.Fatalf("HTML component IDs = %#v, error %v", ids, err)
	}
}

func TestCollectAnthropicStatusIgnoresUnrelatedDuplicateClasses(t *testing.T) {
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	legacyBody := readModelMonitorFixture(t, "anthropic_history.html")
	showcaseBody := readModelMonitorFixture(t, "anthropic_uptime_showcase.json")
	want, err := collectAnthropicStatus(context.Background(), anthropicTestClient(t, summaryBody, legacyBody), "https://status.claude.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"anthropic_history.html", "anthropic_lazy_history.html"} {
		t.Run(fixture, func(t *testing.T) {
			historyBody := []byte(`<div class="status-label" class="description">Updated status</div>` + string(readModelMonitorFixture(t, fixture)))
			client := anthropicShowcaseTestClient(t, summaryBody, historyBody, modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(showcaseBody))), Request: request}, nil
			}))
			status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com")
			if err != nil || !reflect.DeepEqual(status, want) {
				t.Fatalf("unrelated div changed Anthropic collection: status = %#v, error = %v; want %#v", status, err, want)
			}
		})
	}
}

func TestCollectAnthropicStatusRejectsInvalidLazyLayout(t *testing.T) {
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	validBody := string(readModelMonitorFixture(t, "anthropic_lazy_history.html"))
	legacyBody := string(readModelMonitorFixture(t, "anthropic_history.html"))
	apiTag := `<div class='uptime-lazy-placeholder' data-uptime-lazy='claude-api' aria-hidden='true'></div>`
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{name: "unknown ID", body: strings.Replace(validBody, "data-uptime-lazy='claude-api'", "data-uptime-lazy='unknown'", 1), wantError: `包含未知组件 "unknown"`},
		{name: "missing component", body: strings.Replace(validBody, apiTag, "", 1), wantError: "data-uptime-lazy 组件不一致"},
		{name: "extra component", body: validBody + `<div class="uptime-lazy-placeholder" data-uptime-lazy="extra"></div>`, wantError: `包含未知组件 "extra"`},
		{name: "duplicate ID", body: validBody + apiTag, wantError: `组件 ID "claude-api" 重复`},
		{name: "duplicate decoded ID", body: validBody + `<div class="uptime-lazy-placeholder" data-uptime-lazy="claude&#45;api"></div>`, wantError: `组件 ID "claude-api" 重复`},
		{name: "missing ID", body: strings.Replace(validBody, "data-uptime-lazy='claude-api'", "", 1), wantError: "data-uptime-lazy 组件字段无效"},
		{name: "blank ID", body: strings.Replace(validBody, "data-uptime-lazy='claude-api'", "data-uptime-lazy=' '", 1), wantError: "data-uptime-lazy 组件字段无效"},
		{name: "comma in ID", body: strings.Replace(validBody, "data-uptime-lazy='claude-api'", "data-uptime-lazy='claude-api,claude-ai'", 1), wantError: "data-uptime-lazy 组件字段无效"},
		{name: "missing class", body: strings.Replace(validBody, "class='uptime-lazy-placeholder'", "", 1), wantError: "data-uptime-lazy 组件字段无效"},
		{name: "wrong class token", body: strings.Replace(validBody, "class='uptime-lazy-placeholder'", "class='other-uptime-lazy-placeholder'", 1), wantError: "data-uptime-lazy 组件字段无效"},
		{name: "duplicate ID attribute", body: strings.Replace(validBody, "data-uptime-lazy='claude-api'", "data-uptime-lazy='claude-api' DATA-UPTIME-LAZY='claude-ai'", 1), wantError: `字段 "data-uptime-lazy" 重复`},
		{name: "duplicate class attribute", body: strings.Replace(validBody, "class='uptime-lazy-placeholder'", "class='uptime-lazy-placeholder' class='other'", 1), wantError: `字段 "class" 重复`},
		{name: "duplicate class before lazy marker", body: legacyBody + `<div class="other" class="uptime-lazy-placeholder"></div>`, wantError: `字段 "class" 重复`},
		{name: "duplicate class after lazy marker", body: legacyBody + `<div class="uptime-lazy-placeholder" class="other"></div>`, wantError: `字段 "class" 重复`},
		{name: "unclosed tag", body: validBody + `<div class="uptime-lazy-placeholder`, wantError: "data-uptime-lazy 结构无效"},
		{name: "malformed attributes", body: strings.Replace(validBody, "data-uptime-lazy='claude-api'", "data-uptime-lazy=", 1), wantError: "data-uptime-lazy 结构无效"},
		{name: "mixed inline history", body: validBody + legacyBody, wantError: "uptimeData 结构无效"},
		{name: "mixed duplicate inline history", body: validBody + legacyBody + legacyBody, wantError: "uptimeData 结构无效"},
		{name: "mixed damaged inline history", body: validBody + `<script>window.uptimeData={"broken":;</script>`, wantError: "uptimeData 结构无效"},
		{name: "mixed non-object inline history", body: validBody + `<script>window.uptimeData=[];</script>`, wantError: "uptimeData 结构无效"},
		{name: "damaged initialization", body: strings.Replace(validBody, "window.uptimeData || {};", "window.uptimeData || {} unexpected;", 1), wantError: "uptimeData 结构无效"},
		{name: "only comments and scripts", body: strings.Replace(strings.Replace(validBody, apiTag, "", 1), `<div data-uptime-lazy="claude-ai" class="extra uptime-lazy-placeholder" aria-hidden="true"></div>`, "", 1), wantError: "uptimeData 赋值数量为 2，期望 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			showcaseCalls := 0
			client := anthropicShowcaseTestClient(t, summaryBody, []byte(test.body), modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				showcaseCalls++
				return nil, fmt.Errorf("invalid layout must not request showcase history")
			}))
			status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com")
			if err == nil || !strings.Contains(err.Error(), test.wantError) || showcaseCalls != 0 || len(status.Services) != 0 {
				t.Fatalf("invalid layout status = %#v, error = %v, showcase calls = %d; want %q before HTTP", status, err, showcaseCalls, test.wantError)
			}
		})
	}
}

func TestCollectAnthropicStatusRejectsInvalidShowcaseHistory(t *testing.T) {
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	historyBody := readModelMonitorFixture(t, "anthropic_lazy_history.html")
	validBody := string(readModelMonitorFixture(t, "anthropic_uptime_showcase.json"))
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validBody), &payload); err != nil {
		t.Fatal(err)
	}
	var components map[string]json.RawMessage
	if err := json.Unmarshal(payload["timelines"], &components); err != nil {
		t.Fatal(err)
	}
	api := string(components["claude-api"])
	ai := string(components["claude-ai"])
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{name: "damaged JSON", body: `{"timelines":`, wantError: "showcase 不是有效的非空对象"},
		{name: "null response", body: `null`, wantError: "showcase 不是有效的非空对象"},
		{name: "non-object response", body: `[]`, wantError: "showcase 不是有效的非空对象"},
		{name: "empty response", body: `{}`, wantError: "showcase 不是有效的非空对象"},
		{name: "missing timelines", body: `{"components":{}}`, wantError: "响应缺少 timelines 字段"},
		{name: "null timelines", body: `{"timelines":null}`, wantError: "timelines 不是有效的非空对象"},
		{name: "empty timelines", body: `{"timelines":{}}`, wantError: "timelines 不是有效的非空对象"},
		{name: "wrong timelines type", body: `{"timelines":[]}`, wantError: "timelines 不是有效的非空对象"},
		{name: "trailing token", body: validBody + ` true`, wantError: "showcase 不是有效的非空对象"},
		{name: "duplicate timelines", body: `{"timelines":` + string(payload["timelines"]) + `,"timelines":` + string(payload["timelines"]) + `}`, wantError: `key "timelines" 重复`},
		{name: "duplicate decoded timelines", body: `{"timelines":` + string(payload["timelines"]) + `,"\u0074imelines":` + string(payload["timelines"]) + `}`, wantError: `key "timelines" 重复`},
		{name: "duplicate component key", body: `{"timelines":{"claude-api":` + api + `,"claude-api":` + api + `,"claude-ai":` + ai + `}}`, wantError: `组件 key "claude-api" 重复`},
		{name: "duplicate decoded component key", body: `{"timelines":{"claude-api":` + api + `,"\u0063laude-api":` + api + `,"claude-ai":` + ai + `}}`, wantError: `组件 key "claude-api" 重复`},
		{name: "missing component", body: `{"timelines":{"claude-api":` + api + `}}`, wantError: `当前组件 "claude-ai" 缺少历史`},
		{name: "extra component", body: `{"timelines":{"claude-api":` + api + `,"claude-ai":` + ai + `,"extra":` + api + `}}`, wantError: `历史包含未知组件 "extra"`},
		{name: "unknown component", body: `{"timelines":{"claude-api":` + api + `,"unknown":` + ai + `}}`, wantError: `历史包含未知组件 "unknown"`},
		{name: "null component", body: `{"timelines":{"claude-api":null,"claude-ai":` + ai + `}}`, wantError: `组件 "claude-api" 的历史结构无效`},
		{name: "component code mismatch", body: strings.Replace(validBody, `"code":"claude-api"`, `"code":"claude-ai"`, 1), wantError: "历史元数据不一致"},
		{name: "component name mismatch", body: strings.Replace(validBody, `"name":"Claude API (api.anthropic.com)"`, `"name":"Other"`, 1), wantError: "历史元数据不一致"},
		{name: "missing days", body: strings.Replace(validBody, `"days":`, `"other":`, 1), wantError: "days 字段无效"},
		{name: "null days", body: strings.Replace(validBody, `"days":`, `"days":null,"other":`, 1), wantError: "days 字段无效"},
		{name: "empty days", body: strings.Replace(validBody, `"days":`, `"days":[],"other":`, 1), wantError: "days 字段无效"},
		{name: "wrong days shape", body: strings.Replace(validBody, `"days":`, `"days":{},"other":`, 1), wantError: "days 字段无效"},
		{name: "invalid date", body: strings.Replace(validBody, `"2026-07-28"`, `"2026-02-30"`, 1), wantError: "包含无效日期"},
		{name: "duplicate date", body: strings.Replace(validBody, `"2026-07-28"`, `"2026-07-30"`, 1), wantError: "日期 \"2026-07-30\" 重复"},
		{name: "missing outages", body: strings.Replace(validBody, `"outages":{"m":120}`, `"other":{}`, 1), wantError: "outages 字段无效"},
		{name: "null outages", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":null`, 1), wantError: "outages 字段无效"},
		{name: "unknown outage", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":{"unknown":1}`, 1), wantError: "outage \"unknown\" 无效"},
		{name: "invalid outage duration", body: strings.Replace(validBody, `"outages":{"m":120}`, `"outages":{"m":-1}`, 1), wantError: "outage \"m\" 无效"},
		{name: "missing events", body: strings.Replace(validBody, `"related_events":`, `"other_events":`, 1), wantError: "related_events 字段无效"},
		{name: "null events", body: strings.Replace(validBody, `"related_events":`, `"related_events":null,"other_events":`, 1), wantError: "related_events 字段无效"},
		{name: "wrong events shape", body: strings.Replace(validBody, `"related_events":`, `"related_events":{},"other_events":`, 1), wantError: "related_events 字段无效"},
		{name: "blank event name", body: strings.Replace(validBody, `"name":"Major API interruption"`, `"name":" "`, 1), wantError: "related event 缺少名称或 code"},
		{name: "blank event code", body: strings.Replace(validBody, `"code":"event-major"`, `"code":" "`, 1), wantError: "related event 缺少名称或 code"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			showcaseCalls := 0
			client := anthropicShowcaseTestClient(t, summaryBody, historyBody, modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				showcaseCalls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			}))
			status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com")
			if err == nil || !strings.Contains(err.Error(), test.wantError) || showcaseCalls != 1 || len(status.Services) != 0 {
				t.Fatalf("invalid showcase status = %#v, error = %v, calls = %d; want %q", status, err, showcaseCalls, test.wantError)
			}
		})
	}
}

func TestCollectAnthropicStatusBatchesLazyHistoryAndPreservesSummaryOrder(t *testing.T) {
	components := make([]openAIStatusComponent, 61)
	for index := range components {
		components[index] = openAIStatusComponent{ID: fmt.Sprintf("component-%02d", index), Name: fmt.Sprintf("Component %d", index), Status: "operational"}
	}
	components[0].ID = "component/0?mode=bad&extra=1 +#"
	summaryBody := mutateAnthropicSummary(t, func(payload map[string]any) { payload["components"] = components })
	var history strings.Builder
	pageIDs := make([]string, 0, len(components))
	for index := len(components) - 1; index >= 0; index-- {
		component := components[index]
		fmt.Fprintf(&history, `<div class="uptime-lazy-placeholder" data-uptime-lazy="%s"></div>`, html.EscapeString(component.ID))
		pageIDs = append(pageIDs, component.ID)
	}
	for _, mode := range []string{"success", "failure in second batch", "component from another batch"} {
		t.Run(mode, func(t *testing.T) {
			showcaseCalls := 0
			client := anthropicShowcaseTestClient(t, summaryBody, []byte(history.String()), modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				start := showcaseCalls * 60
				showcaseCalls++
				if showcaseCalls > 2 {
					return nil, fmt.Errorf("unexpected extra showcase request")
				}
				wantIDs := pageIDs[start:min(start+60, len(pageIDs))]
				ids := strings.Split(request.URL.Query().Get("components"), ",")
				if len(request.URL.Query()) != 1 || !equalStrings(ids, wantIDs) {
					return nil, fmt.Errorf("showcase batch IDs = %#v, want %#v; query = %q", ids, wantIDs, request.URL.RawQuery)
				}
				if showcaseCalls == 2 && mode == "failure in second batch" {
					return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable")), Request: request}, nil
				}
				timelines := make(map[string]any, len(ids))
				for _, id := range ids {
					for index, component := range components {
						if component.ID != id {
							continue
						}
						days := []map[string]any{{"date": "2026-07-30", "outages": map[string]any{}, "related_events": []any{}}}
						if index == 0 {
							days = make([]map[string]any, 95)
							for day := range days {
								days[day] = map[string]any{"date": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 94-day).Format("2006-01-02"), "outages": map[string]any{}, "related_events": []any{}}
							}
						}
						timelines[id] = map[string]any{"component": map[string]any{"code": id, "name": component.Name}, "days": days}
					}
				}
				if showcaseCalls == 2 && mode == "component from another batch" {
					timelines[pageIDs[0]] = timelines[ids[0]]
				}
				body, err := json.Marshal(map[string]any{"timelines": timelines})
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
			}))
			status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com/")
			if showcaseCalls != 2 {
				t.Fatalf("showcase calls = %d, want 2; error = %v", showcaseCalls, err)
			}
			if mode != "success" {
				wantError := "HTTP 502"
				if mode == "component from another batch" {
					wantError = "历史包含未知组件"
				}
				if err == nil || !strings.Contains(err.Error(), wantError) || len(status.Services) != 0 {
					t.Fatalf("incomplete batch status = %#v, error = %v; want %q", status, err, wantError)
				}
				return
			}
			if err != nil || len(status.Services) != len(components) {
				t.Fatalf("batched collection = %#v, error = %v", status, err)
			}
			for index, service := range status.Services {
				if service.ID != components[index].ID || service.Name != components[index].Name || service.UptimePercent != nil || service.Samples == nil {
					t.Fatalf("service %d = %#v, want %#v", index, service, components[index])
				}
			}
			samples := status.Services[0].Samples
			if len(samples) != 90 || samples[0].Timestamp.Format("2006-01-02") != "2026-01-06" || samples[89].Timestamp.Format("2006-01-02") != "2026-04-05" {
				t.Fatalf("batched history must preserve the latest 90 days: %#v", samples)
			}
		})
	}
}

func TestCollectAnthropicStatusRejectsShowcaseTransportFailures(t *testing.T) {
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	historyBody := readModelMonitorFixture(t, "anthropic_lazy_history.html")
	tests := []struct {
		name         string
		statusCode   int
		contentType  string
		body         string
		requestError error
		cancel       bool
		wantError    string
	}{
		{name: "non-2xx", statusCode: http.StatusBadGateway, contentType: "application/json", body: `{}`, wantError: "HTTP 502"},
		{name: "content type", statusCode: http.StatusOK, contentType: "text/html", body: `{}`, wantError: "不支持的内容类型"},
		{name: "content type prefix", statusCode: http.StatusOK, contentType: "application/jsonp", body: `{}`, wantError: "不支持的内容类型"},
		{name: "oversized body", statusCode: http.StatusOK, contentType: "application/json", body: strings.Repeat(" ", modelMonitorMaxBodyBytes+1), wantError: "超过 8 MiB 限制"},
		{name: "canceled showcase", cancel: true, wantError: "context canceled"},
		{name: "timeout", requestError: context.DeadlineExceeded, wantError: "context deadline exceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			type contextKey struct{}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "showcase-context"))
			defer cancel()
			showcaseCalls := 0
			client := anthropicShowcaseTestClient(t, summaryBody, historyBody, modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				showcaseCalls++
				if request.Context().Value(contextKey{}) != "showcase-context" {
					return nil, fmt.Errorf("showcase request lost its caller context")
				}
				if test.cancel {
					cancel()
					return nil, request.Context().Err()
				}
				if test.requestError != nil {
					return nil, test.requestError
				}
				return &http.Response{StatusCode: test.statusCode, Header: http.Header{"Content-Type": []string{test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			}))
			status, err := collectAnthropicStatus(ctx, client, "https://status.claude.com")
			if err == nil || !strings.Contains(err.Error(), test.wantError) || showcaseCalls != 1 || len(status.Services) != 0 {
				t.Fatalf("failed showcase status = %#v, error = %v, calls = %d; want %q without a retry", status, err, showcaseCalls, test.wantError)
			}
		})
	}
}

func TestCollectAnthropicStatusKeepsShowcaseRedirectLimits(t *testing.T) {
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	historyBody := readModelMonitorFixture(t, "anthropic_lazy_history.html")
	for _, test := range []struct {
		name      string
		location  string
		wantError string
		wantCalls int
	}{
		{name: "HTTP downgrade", location: "http://status.claude.com/uptime_showcase", wantError: "重定向必须使用 HTTPS", wantCalls: 1},
		{name: "redirect loop", location: "https://status.claude.com/uptime_showcase", wantError: "重定向次数过多", wantCalls: modelMonitorMaxRedirects},
	} {
		t.Run(test.name, func(t *testing.T) {
			showcaseCalls := 0
			fakeClient := anthropicShowcaseTestClient(t, summaryBody, historyBody, modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				showcaseCalls++
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{test.location}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
			}))
			client, err := newModelMonitorBuiltinHTTPClient(ModelMonitorProxyConfig{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			client.Transport = fakeClient.Transport
			status, err := collectAnthropicStatus(context.Background(), client, "https://status.claude.com")
			if err == nil || !strings.Contains(err.Error(), test.wantError) || showcaseCalls != test.wantCalls || len(status.Services) != 0 {
				t.Fatalf("redirected showcase status = %#v, error = %v, calls = %d; want %q after %d calls", status, err, showcaseCalls, test.wantError, test.wantCalls)
			}
		})
	}
}

func TestCollectDeepSeekStatusUsesHostRoutedFlashcatPage(t *testing.T) {
	client := deepseekFlashcatTestClient(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	status, err := collectDeepSeekStatus(context.Background(), client, modelMonitorDeepSeekCollectionBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if status.OverallStatus != "partial_outage" || len(status.Services) != 2 || len(status.Groups) != 1 || len(status.Incidents) != 1 {
		t.Fatalf("unexpected DeepSeek collection: %#v", status)
	}
	if status.SourceUpdatedAt == nil || !status.SourceUpdatedAt.Equal(time.UnixMilli(1785868658945).UTC()) {
		t.Fatalf("DeepSeek source timestamp = %#v", status.SourceUpdatedAt)
	}
	if status.Services[0].ID != "01KY4MVS8BM3F9JSYWACGQVG7A" || status.Services[0].Status != "operational" || status.Services[1].ID != "01KY4MVS8BSBSVW6053QJ37RJE" || status.Services[1].Status != "degraded_performance" || status.Services[1].UptimePercent == nil || *status.Services[1].UptimePercent != 99.77 {
		t.Fatalf("DeepSeek standalone services = %#v", status.Services)
	}
	if status.Services[0].Name == "API 服务 (API Service)" || status.Services[1].Name == "网页对话服务 (Web Chat Service)" {
		t.Fatalf("hidden legacy DeepSeek components leaked into services: %#v", status.Services)
	}
	group := status.Groups[0]
	if group.ID != "01KY4ND2MQAQKEAGJKTNGREKBR" || group.Name != "对话服务(Chat Service)" || group.Status != "partial_outage" || group.UptimePercent == nil || *group.UptimePercent != 99.76 || len(group.Services) != 5 {
		t.Fatalf("DeepSeek chat group = %#v", group)
	}
	wantChildIDs := []string{
		"01KY4ND2PNYT9FY5W4ZH80VGJ4",
		"01KY4ND2PN1CCNW2MFT5VW713H",
		"01KY4ND2PNJ6MFA4VJ0DSN6M2J",
		"01KY4ND2PNNFFY6QKV67WFJW8N",
		"01KY4ND2PN6EFSJ4RDYDJYPMNK",
	}
	for index, service := range group.Services {
		if service.ID != wantChildIDs[index] {
			t.Fatalf("DeepSeek group component order = %#v, want %#v", group.Services, wantChildIDs)
		}
	}
	if group.Services[1].Status != "partial_outage" || group.Services[1].UptimePercent == nil || *group.Services[1].UptimePercent != 99.72 {
		t.Fatalf("DeepSeek expert mode = %#v", group.Services[1])
	}
	impactDay := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	flashSample := findModelMonitorSample(t, status.Services[1].Samples, impactDay)
	if flashSample.Status != "degraded_performance" || !equalStrings(flashSample.RelatedIncidents, []string{"Flash API degraded"}) {
		t.Fatalf("DeepSeek Flash history = %#v", flashSample)
	}
	groupSample := findModelMonitorSample(t, group.Samples, impactDay)
	if groupSample.Status != "major_outage" || !equalStrings(groupSample.RelatedIncidents, []string{"Expert mode partial outage", "Chat service outage"}) {
		t.Fatalf("DeepSeek group history = %#v", groupSample)
	}
	if status.Incidents[0].ID != "9001" || status.Incidents[0].Impact != "partial_outage" || status.Incidents[0].UpdatedAt == nil {
		t.Fatalf("DeepSeek current incident = %#v", status.Incidents)
	}
}

func TestParseDeepSeekFlashcatPageAcceptsReactFlightPropArrayFrames(t *testing.T) {
	body := deepSeekFlashcatReactFlightPropArrayFrames(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	status, err := parseDeepSeekFlashcatPage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 2 || len(status.Groups) != 1 || len(status.Groups[0].Services) != 5 || len(status.Incidents) != 1 {
		t.Fatalf("unexpected DeepSeek React Flight props result: %#v", status)
	}
	if status.SourceUpdatedAt == nil || !status.SourceUpdatedAt.Equal(time.UnixMilli(1785868658945).UTC()) {
		t.Fatalf("DeepSeek React Flight props timestamp = %#v", status.SourceUpdatedAt)
	}
}

func TestParseDeepSeekFlashcatPageRejectsDuplicateNestedObjectFields(t *testing.T) {
	fixture := deepSeekFlashcatReactFlightPropArrayFrames(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	for _, test := range []struct {
		name        string
		frameMarker string
		from        string
		to          string
	}{
		{
			name:        "page config components",
			frameMarker: `"initialPageConfig"`,
			from:        `"initialPageConfig":{"components":[`,
			to:          `"initialPageConfig":{"components":[],"components":[`,
		},
		{
			name:        "current active changes",
			frameMarker: `"initialDataUpdatedAt"`,
			from:        `"active_changes":[`,
			to:          `"active_changes":[],"active_changes":[`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := mutateDeepSeekFlashcatRawFrame(t, fixture, test.frameMarker, func(frame string) string {
				result := strings.Replace(frame, test.from, test.to, 1)
				if result == frame {
					t.Fatalf("DeepSeek fixture did not contain %q", test.from)
				}
				return result
			})
			if _, err := parseDeepSeekFlashcatPage(body); err == nil {
				t.Fatalf("duplicate DeepSeek nested field %s should fail", test.name)
			}
		})
	}
}

func TestParseDeepSeekFlashcatPageRejectsMalformedHistory(t *testing.T) {
	body := strings.Replace(
		string(readModelMonitorFixture(t, "deepseek_flashcat.html")),
		`\"component_uptimes\":[`,
		`\"component_uptimes\":null,\"ignored\":[`,
		1,
	)
	if _, err := parseDeepSeekFlashcatPage([]byte(body)); err == nil || !strings.Contains(err.Error(), "DeepSeek") {
		t.Fatalf("malformed DeepSeek Flashcat history error = %v", err)
	}
}

func TestParseDeepSeekFlashcatPageRejectsDuplicateRequiredHistoryFields(t *testing.T) {
	fixture := deepSeekFlashcatReactFlightPropArrayFrames(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	for _, key := range []string{
		"component_uptimes",
		"section_uptimes",
		"component_impacts",
		"section_impacts",
		"linked_changes",
	} {
		t.Run(key, func(t *testing.T) {
			marker := `"` + key + `":[`
			body := mutateDeepSeekFlashcatRawFrame(t, fixture, marker, func(frame string) string {
				return strings.Replace(frame, marker, `"`+key+`":[],"`+key+`":[`, 1)
			})
			if _, err := parseDeepSeekFlashcatPage(body); err == nil {
				t.Fatalf("duplicate DeepSeek history field %q should fail", key)
			}
		})
	}
}

func TestParseDeepSeekFlashcatPageValidatesFullUpdatedAtNumber(t *testing.T) {
	fixture := deepSeekFlashcatReactFlightPropArrayFrames(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	body := mutateDeepSeekFlashcatUpdatedAtNumber(t, fixture, "1.785868658945e12")
	status, err := parseDeepSeekFlashcatPage(body)
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceUpdatedAt == nil || !status.SourceUpdatedAt.Equal(time.UnixMilli(1785868658945).UTC()) {
		t.Fatalf("DeepSeek scientific-notation timestamp = %#v", status.SourceUpdatedAt)
	}

	for _, value := range []string{"1785868658945.5", "1e-3", "1785868658945invalid"} {
		t.Run(value, func(t *testing.T) {
			body := mutateDeepSeekFlashcatUpdatedAtNumber(t, fixture, value)
			if _, err := parseDeepSeekFlashcatPage(body); err == nil {
				t.Fatalf("DeepSeek updated-at value %q should fail", value)
			}
		})
	}
}

func TestParseDeepSeekFlashcatPageOmitsHiddenSectionChildren(t *testing.T) {
	const hiddenSectionID = "hidden-chat-section"
	const hiddenComponentID = "hidden-chat-component"
	body := readModelMonitorFixture(t, "deepseek_flashcat.html")
	body = mutateDeepSeekFlashcatFrame(t, body, `"initialPageConfig"`, func(payload map[string]any) {
		config := payload["initialPageConfig"].(map[string]any)
		config["sections"] = append(config["sections"].([]any), map[string]any{
			"section_id": hiddenSectionID,
			"name":       "Hidden Chat",
			"order_id":   1,
			"hide_all":   true,
		})
		config["components"] = append(config["components"].([]any), map[string]any{
			"component_id":            hiddenComponentID,
			"section_id":              hiddenSectionID,
			"name":                    "Hidden Chat Service",
			"available_since_seconds": 1706745600,
			"order_id":                1,
		})
	})
	body = mutateDeepSeekFlashcatFrame(t, body, `"initialDataUpdatedAt"`, func(payload map[string]any) {
		page := payload["initialData"].(map[string]any)["page"].(map[string]any)
		page["components"] = append(page["components"].([]any), map[string]any{
			"component_id": hiddenComponentID,
			"name":         "Hidden Chat Service",
		})
		page["active_changes"] = append(page["active_changes"].([]any), map[string]any{
			"change_id": 9002,
			"title":     "Hidden Chat Incident",
			"status":    "investigating",
			"updates": []any{map[string]any{
				"at_seconds": 1785868200,
				"component_changes": []any{map[string]any{
					"component_id": hiddenComponentID,
					"status":       "partial_outage",
				}},
			}},
		})
	})
	body = mutateDeepSeekFlashcatFrame(t, body, `"component_uptimes"`, func(payload map[string]any) {
		history := payload["initialData"].(map[string]any)
		history["section_impacts"] = append(history["section_impacts"].([]any), map[string]any{
			"section_id":       hiddenSectionID,
			"change_id":        104,
			"start_at_seconds": 1785800000,
			"end_at_seconds":   1785803600,
			"status":           "full_outage",
		})
		history["section_uptimes"] = append(history["section_uptimes"].([]any), map[string]any{
			"section_id":              hiddenSectionID,
			"uptime":                  0,
			"available_since_seconds": 1706745600,
		})
		history["component_impacts"] = append(history["component_impacts"].([]any), map[string]any{
			"component_id":     hiddenComponentID,
			"section_id":       hiddenSectionID,
			"change_id":        104,
			"start_at_seconds": 1785800000,
			"end_at_seconds":   1785803600,
			"status":           "full_outage",
		})
		history["component_uptimes"] = append(history["component_uptimes"].([]any), map[string]any{
			"component_id":            hiddenComponentID,
			"section_id":              hiddenSectionID,
			"uptime":                  0,
			"available_since_seconds": 1706745600,
		})
		history["linked_changes"] = append(history["linked_changes"].([]any), map[string]any{
			"id":    104,
			"type":  "incident",
			"title": "Hidden Chat Incident",
		})
	})

	status, err := parseDeepSeekFlashcatPage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 2 || len(status.Groups) != 1 || len(status.Groups[0].Services) != 5 {
		t.Fatalf("hidden section changed visible DeepSeek hierarchy: %#v", status)
	}
	if len(status.Incidents) != 1 || status.Incidents[0].ID != "9001" {
		t.Fatalf("hidden section incident leaked into current incidents: %#v", status.Incidents)
	}
	for _, service := range append(append([]ModelMonitorServiceStatus{}, status.Services...), status.Groups[0].Services...) {
		if service.ID == hiddenComponentID {
			t.Fatalf("hidden section component leaked into DeepSeek response: %#v", status)
		}
	}
	if status.Groups[0].ID == hiddenSectionID {
		t.Fatalf("hidden section leaked into DeepSeek response: %#v", status.Groups)
	}
}

func TestParseDeepSeekFlashcatPageAcceptsNoActiveChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "empty array", mutate: func(current map[string]any) { current["active_changes"] = []any{} }},
		{name: "null", mutate: func(current map[string]any) { current["active_changes"] = nil }},
		{name: "omitted", mutate: func(current map[string]any) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := mutateDeepSeekFlashcatFrame(t, readModelMonitorFixture(t, "deepseek_flashcat.html"), `"initialDataUpdatedAt"`, func(payload map[string]any) {
				current := payload["initialData"].(map[string]any)
				delete(current["page"].(map[string]any), "active_changes")
				test.mutate(current)
			})
			status, err := parseDeepSeekFlashcatPage(body)
			if err != nil {
				t.Fatal(err)
			}
			if status.OverallStatus != "operational" || len(status.Incidents) != 0 || status.Groups[0].Status != "operational" {
				t.Fatalf("DeepSeek active changes %s = %#v", test.name, status)
			}
			for _, service := range append(append([]ModelMonitorServiceStatus{}, status.Services...), status.Groups[0].Services...) {
				if service.Status != "operational" {
					t.Fatalf("DeepSeek active changes %s left service affected: %#v", test.name, service)
				}
			}
		})
	}
}

func TestParseDeepSeekFlashcatPageReadsTopLevelActiveChanges(t *testing.T) {
	body := mutateDeepSeekFlashcatFrame(t, readModelMonitorFixture(t, "deepseek_flashcat.html"), `"initialDataUpdatedAt"`, func(payload map[string]any) {
		current := payload["initialData"].(map[string]any)
		page := current["page"].(map[string]any)
		current["active_changes"] = page["active_changes"]
		delete(page, "active_changes")
	})
	status, err := parseDeepSeekFlashcatPage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Incidents) != 1 || status.Incidents[0].ID != "9001" || status.Services[1].Status != "degraded_performance" || status.Groups[0].Services[1].Status != "partial_outage" {
		t.Fatalf("DeepSeek top-level active changes = %#v", status)
	}
}

func TestParseDeepSeekFlashcatPageRejectsInvalidActiveChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "object", mutate: func(current map[string]any) { current["active_changes"] = map[string]any{} }},
		{name: "string", mutate: func(current map[string]any) { current["active_changes"] = "not an array" }},
		{name: "boolean", mutate: func(current map[string]any) { current["active_changes"] = false }},
		{name: "number", mutate: func(current map[string]any) { current["active_changes"] = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := mutateDeepSeekFlashcatFrame(t, readModelMonitorFixture(t, "deepseek_flashcat.html"), `"initialDataUpdatedAt"`, func(payload map[string]any) {
				current := payload["initialData"].(map[string]any)
				delete(current["page"].(map[string]any), "active_changes")
				test.mutate(current)
			})
			if _, err := parseDeepSeekFlashcatPage(body); err == nil {
				t.Fatalf("DeepSeek active changes %s should fail", test.name)
			}
		})
	}
}

func TestParseDeepSeekFlashcatPageRejectsMissingOrDuplicateRequiredFrames(t *testing.T) {
	frames, err := deepSeekFlashcatNextFlightFrames(readModelMonitorFixture(t, "deepseek_flashcat.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"initialPageConfig"`, `"initialDataUpdatedAt"`, `"component_uptimes"`} {
		t.Run("missing "+marker, func(t *testing.T) {
			missing := append([]string{}, frames...)
			for index, frame := range missing {
				if strings.Contains(frame, marker) {
					missing[index] = strings.Replace(frame, marker, `"missing"`, 1)
					if _, err := parseDeepSeekFlashcatPage(deepSeekFlashcatHTMLFromFrames(t, missing)); err == nil {
						t.Fatalf("missing required frame %s should fail", marker)
					}
					return
				}
			}
			t.Fatalf("fixture is missing required marker %s", marker)
		})
		t.Run("duplicate "+marker, func(t *testing.T) {
			for _, frame := range frames {
				if strings.Contains(frame, marker) {
					duplicated := append(append([]string{}, frames...), frame)
					if _, err := parseDeepSeekFlashcatPage(deepSeekFlashcatHTMLFromFrames(t, duplicated)); err == nil {
						t.Fatalf("duplicate required frame %s should fail", marker)
					}
					return
				}
			}
			t.Fatalf("fixture is missing required marker %s", marker)
		})
	}
}

func mutateDeepSeekFlashcatFrame(t *testing.T, body []byte, marker string, mutate func(map[string]any)) []byte {
	t.Helper()
	frames, err := deepSeekFlashcatNextFlightFrames(body)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for index, frame := range frames {
		if !strings.Contains(frame, marker) {
			continue
		}
		separator := strings.IndexByte(frame, ':')
		if separator <= 0 {
			t.Fatalf("DeepSeek fixture frame %q has no payload separator", marker)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(frame[separator+1:]), &payload); err != nil {
			t.Fatal(err)
		}
		mutate(payload)
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		frames[index] = frame[:separator+1] + string(encoded)
		matched++
	}
	if matched != 1 {
		t.Fatalf("DeepSeek fixture frame %q matches = %d, want 1", marker, matched)
	}
	return deepSeekFlashcatHTMLFromFrames(t, frames)
}

func deepSeekFlashcatReactFlightPropArrayFrames(t *testing.T, body []byte) []byte {
	t.Helper()
	frames, err := deepSeekFlashcatNextFlightFrames(body)
	if err != nil {
		t.Fatal(err)
	}
	for index, frame := range frames {
		separator := strings.IndexByte(frame, ':')
		if separator <= 0 {
			t.Fatalf("DeepSeek fixture frame %q has no payload separator", frame)
		}
		props := strings.TrimSpace(frame[separator+1:])
		if !strings.HasPrefix(props, "{") {
			t.Fatalf("DeepSeek fixture frame %q does not contain object props", frame)
		}
		frames[index] = `c:["$","$L1b",null,` + props + `]`
	}
	return deepSeekFlashcatHTMLFromFrames(t, frames)
}

func mutateDeepSeekFlashcatRawFrame(t *testing.T, body []byte, marker string, mutate func(string) string) []byte {
	t.Helper()
	frames, err := deepSeekFlashcatNextFlightFrames(body)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for index, frame := range frames {
		if !strings.Contains(frame, marker) {
			continue
		}
		frames[index] = mutate(frame)
		matched++
	}
	if matched != 1 {
		t.Fatalf("DeepSeek fixture raw frame %q matches = %d, want 1", marker, matched)
	}
	return deepSeekFlashcatHTMLFromFrames(t, frames)
}

func mutateDeepSeekFlashcatUpdatedAtNumber(t *testing.T, body []byte, replacement string) []byte {
	t.Helper()
	const marker = `"initialDataUpdatedAt":`
	return mutateDeepSeekFlashcatRawFrame(t, body, marker, func(frame string) string {
		start := strings.Index(frame, marker) + len(marker)
		for start < len(frame) && (frame[start] == ' ' || frame[start] == '\n' || frame[start] == '\r' || frame[start] == '\t') {
			start++
		}
		end := start
		for end < len(frame) && (frame[end] == '-' || frame[end] == '+' || frame[end] == '.' || frame[end] == 'e' || frame[end] == 'E' || (frame[end] >= '0' && frame[end] <= '9')) {
			end++
		}
		if start == end {
			t.Fatalf("DeepSeek fixture updated-at value is missing")
		}
		return frame[:start] + replacement + frame[end:]
	})
}

func deepSeekFlashcatHTMLFromFrames(t *testing.T, frames []string) []byte {
	t.Helper()
	var body strings.Builder
	body.WriteString("<!doctype html><html><body>")
	for _, frame := range frames {
		encoded, err := json.Marshal([]any{1, frame})
		if err != nil {
			t.Fatal(err)
		}
		body.WriteString("<script>self.__next_f.push(")
		body.Write(encoded)
		body.WriteString(")</script>")
	}
	body.WriteString("</body></html>")
	return []byte(body.String())
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
	deepseekClient := deepseekFlashcatTestClient(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	var seenHosts sync.Map
	transport := &modelMonitorCloseTrackingTransport{roundTripper: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		seenHosts.Store(request.URL.Host, true)
		if request.URL.Host == "status.input.im" {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(readModelMonitorFixture(t, "input_im_status.json")))), Request: request}, nil
		}
		if request.URL.Host == "status.claude.com" {
			return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"upstream down"}`)), Request: request}, nil
		}
		if request.URL.Host == "statuspage.flashcat.cloud" {
			return deepseekClient.Transport.RoundTrip(request)
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
	for _, host := range []string{"status.input.im", "status.openai.com", "status.claude.com", "statuspage.flashcat.cloud"} {
		if _, ok := seenHosts.Load(host); !ok {
			t.Fatalf("model monitor source %q did not use the configured client", host)
		}
	}
	if len(response.Sources) != 4 || response.Sources[0].ID != "ai-input-im" || response.Sources[1].ID != "openai" || response.Sources[2].ID != "anthropic" || response.Sources[3].ID != "deepseek" {
		t.Fatalf("unexpected source order: %#v", response.Sources)
	}
	if response.Sources[2].Name != "Claude Status" || response.Sources[2].StatusPageURL != "https://status.claude.com" {
		t.Fatalf("unexpected Claude Status identity: %#v", response.Sources[2])
	}
	if response.Sources[0].CollectionState != modelMonitorCollectionOK || response.Sources[1].CollectionState != modelMonitorCollectionOK || response.Sources[2].CollectionState != modelMonitorCollectionError || response.Sources[3].CollectionState != modelMonitorCollectionOK {
		t.Fatalf("unexpected collection states: %#v", response.Sources)
	}
	for _, source := range response.Sources {
		if source.Groups == nil || source.Services == nil || source.Incidents == nil {
			t.Fatalf("source arrays must be non-nil: %#v", source)
		}
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources", nil, cookies, http.StatusNotFound, nil)
}

func TestModelMonitorSourceSettingsAndSingleSourceRoutes(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	openAIClient := openAITestClient(t, openAITestSummary())
	deepseekClient := deepseekFlashcatTestClient(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	transport := &modelMonitorCloseTrackingTransport{roundTripper: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "status.openai.com":
			return openAIClient.Transport.RoundTrip(request)
		case "statuspage.flashcat.cloud":
			return deepseekClient.Transport.RoundTrip(request)
		default:
			return nil, fmt.Errorf("unexpected model monitor host %q", request.URL.Host)
		}
	})}
	fakeClient := &http.Client{Transport: transport}
	factoryCalls := 0
	app.modelMonitorHTTPClient = func(ModelMonitorProxyConfig) (*http.Client, error) {
		factoryCalls++
		return fakeClient, nil
	}

	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	var settings ModelMonitorSettings
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/settings", nil, cookies, http.StatusOK, &settings)
	wantDefaultIDs := []string{"ai-input-im", "openai", "anthropic", "deepseek"}
	if !equalStrings(settings.EnabledSourceIDs, wantDefaultIDs) || len(settings.Sources) != 4 {
		t.Fatalf("default model monitor settings = %#v", settings)
	}
	for _, source := range settings.Sources {
		if !source.Enabled {
			t.Fatalf("default source %q is disabled", source.ID)
		}
	}
	if settings.Sources[3].ID != "deepseek" || settings.Sources[3].StatusPageURL != "https://status.deepseek.com" || settings.Sources[3].HistoryGranularity != modelMonitorHistoryDay || settings.Sources[3].HistoryWindowLabel != "90 days" {
		t.Fatalf("DeepSeek settings metadata = %#v", settings.Sources[3])
	}

	modelMonitorRequest(t, handler, http.MethodPost, "/api/model-monitor/settings", nil, cookies, http.StatusMethodNotAllowed, nil)
	for _, payload := range []map[string]any{
		{},
		{"enabled_source_ids": nil},
		{"enabled_source_ids": []string{"deepseek", "deepseek"}},
		{"enabled_source_ids": []string{"unknown"}},
		{"enabled_source_ids": []string{" "}},
	} {
		modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/settings", payload, cookies, http.StatusUnprocessableEntity, nil)
	}
	settings = ModelMonitorSettings{}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/settings", nil, cookies, http.StatusOK, &settings)
	if !equalStrings(settings.EnabledSourceIDs, wantDefaultIDs) {
		t.Fatalf("invalid settings update changed persisted order: %#v", settings.EnabledSourceIDs)
	}

	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/settings", map[string]any{"enabled_source_ids": []string{"deepseek", "openai"}}, cookies, http.StatusOK, &settings)
	if !equalStrings(settings.EnabledSourceIDs, []string{"deepseek", "openai"}) {
		t.Fatalf("saved settings order = %#v", settings.EnabledSourceIDs)
	}
	if len(settings.Sources) != 4 || settings.Sources[0].ID != "deepseek" || !settings.Sources[0].Enabled || settings.Sources[1].ID != "openai" || !settings.Sources[1].Enabled || settings.Sources[2].ID != "ai-input-im" || settings.Sources[2].Enabled || settings.Sources[3].ID != "anthropic" || settings.Sources[3].Enabled {
		t.Fatalf("saved settings projection = %#v", settings.Sources)
	}
	appCfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	appCfg.Collector.QueueName = "source-settings-isolation"
	if err := app.saveConfig(context.Background(), appCfg); err != nil {
		t.Fatal(err)
	}
	persistedSettings, err := app.loadModelMonitorSourceSettings(context.Background())
	if err != nil || !equalStrings(persistedSettings.EnabledSourceIDs, []string{"deepseek", "openai"}) {
		t.Fatalf("generic config save overwrote source settings: %#v / %v", persistedSettings, err)
	}

	var source ModelMonitorSourceStatus
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources/deepseek", nil, cookies, http.StatusOK, &source)
	if source.ID != "deepseek" || source.CollectionState != modelMonitorCollectionOK || factoryCalls != 1 || transport.closeCalls != 1 {
		t.Fatalf("single DeepSeek source = %#v; factory/close calls = %d/%d", source, factoryCalls, transport.closeCalls)
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources/anthropic", nil, cookies, http.StatusNotFound, nil)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources/unknown", nil, cookies, http.StatusNotFound, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/sources/deepseek", nil, cookies, http.StatusMethodNotAllowed, nil)
	if factoryCalls != 1 {
		t.Fatalf("disabled or unknown source route collected a source; factory calls = %d", factoryCalls)
	}

	var aggregate ModelMonitorResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, cookies, http.StatusOK, &aggregate)
	if factoryCalls != 2 || transport.closeCalls != 2 || len(aggregate.Sources) != 2 || aggregate.Sources[0].ID != "deepseek" || aggregate.Sources[1].ID != "openai" {
		t.Fatalf("ordered aggregate = %#v; factory/close calls = %d/%d", aggregate.Sources, factoryCalls, transport.closeCalls)
	}

	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/settings", map[string]any{"enabled_source_ids": []string{}}, cookies, http.StatusOK, &settings)
	if settings.EnabledSourceIDs == nil || len(settings.EnabledSourceIDs) != 0 {
		t.Fatalf("empty enabled source IDs = %#v", settings.EnabledSourceIDs)
	}
	aggregate = ModelMonitorResponse{}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, cookies, http.StatusOK, &aggregate)
	if aggregate.Sources == nil || len(aggregate.Sources) != 0 || factoryCalls != 2 {
		t.Fatalf("empty aggregate = %#v; factory calls = %d", aggregate.Sources, factoryCalls)
	}
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources/deepseek", nil, cookies, http.StatusNotFound, nil)
}

func TestModelMonitorSourceSettingsRejectCorruptPersistedValuesAndSurviveRestart(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}

	ids := []string{"deepseek", "openai"}
	if _, err := app.updateModelMonitorSourceSettings(context.Background(), modelMonitorSourceSettingsPayload{EnabledSourceIDs: &ids}); err != nil {
		app.Close()
		t.Fatalf("save source settings: %v", err)
	}
	app.Close()

	app, err = NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	settings, err := app.loadModelMonitorSourceSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(settings.EnabledSourceIDs, ids) {
		t.Fatalf("source settings after restart = %#v, want %#v", settings.EnabledSourceIDs, ids)
	}
	for _, value := range []string{`not JSON`, `null`, `["openai","openai"]`, `["unknown"]`, `[""]`} {
		if _, err := app.db.Exec(`UPDATE app_settings SET model_monitor_enabled_source_ids = ? WHERE id = 1`, value); err != nil {
			t.Fatal(err)
		}
		if _, err := app.loadModelMonitorSourceSettings(context.Background()); err == nil {
			t.Fatalf("corrupt persisted value %q should fail", value)
		}
	}
}

func TestModelMonitorRoutesReturnValidationErrorForCorruptSourceSettings(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	factoryCalls := 0
	app.modelMonitorHTTPClient = func(ModelMonitorProxyConfig) (*http.Client, error) {
		factoryCalls++
		return nil, fmt.Errorf("collector client must not be created for corrupt source settings")
	}
	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	routes := []struct {
		name   string
		target string
	}{
		{name: "settings", target: "/api/model-monitor/settings"},
		{name: "aggregate", target: "/api/model-monitor"},
		{name: "source", target: "/api/model-monitor/sources/openai"},
	}
	tests := []struct {
		name            string
		persistedValue  string
		expectedMessage string
	}{
		{
			name:            "malformed JSON",
			persistedValue:  `not JSON`,
			expectedMessage: "模型监控来源配置无效: enabled_source_ids 必须是 JSON 字符串数组",
		},
		{
			name:            "duplicate ID",
			persistedValue:  "[\"openai\",\"openai\"]",
			expectedMessage: "模型监控来源配置无效: 模型监控来源 ID \"openai\" 重复",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := app.db.Exec(`UPDATE app_settings SET model_monitor_enabled_source_ids = ? WHERE id = 1`, test.persistedValue); err != nil {
				t.Fatal(err)
			}
			for _, route := range routes {
				t.Run(route.name, func(t *testing.T) {
					var response struct {
						Detail struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"detail"`
					}
					modelMonitorRequest(t, handler, http.MethodGet, route.target, nil, cookies, http.StatusUnprocessableEntity, &response)
					if response.Detail.Code != "validation_error" || response.Detail.Message != test.expectedMessage {
						t.Fatalf("%s error = %#v, want validation_error / %q", route.target, response.Detail, test.expectedMessage)
					}
				})
			}
		})
	}
	if factoryCalls != 0 {
		t.Fatalf("collector client factory calls = %d, want 0", factoryCalls)
	}
}

func TestModelMonitorAggregationIsolatesAnthropicLazyHistoryFailure(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	openAIClient := openAITestClient(t, openAITestSummary())
	deepseekClient := deepseekFlashcatTestClient(t, readModelMonitorFixture(t, "deepseek_flashcat.html"))
	inputBody := string(readModelMonitorFixture(t, "input_im_status.json"))
	summaryBody := readModelMonitorFixture(t, "anthropic_summary.json")
	historyBody := readModelMonitorFixture(t, "anthropic_lazy_history.html")
	showcaseBody := string(readModelMonitorFixture(t, "anthropic_uptime_showcase.json"))
	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		wantState  string
	}{
		{name: "success", statusCode: http.StatusOK, body: showcaseBody, wantState: modelMonitorCollectionOK},
		{name: "invalid timeline", statusCode: http.StatusOK, body: `{"timelines":null}`, wantState: modelMonitorCollectionError},
		{name: "showcase HTTP failure", statusCode: http.StatusBadGateway, body: `{}`, wantState: modelMonitorCollectionError},
	} {
		t.Run(test.name, func(t *testing.T) {
			anthropicClient := anthropicShowcaseTestClient(t, summaryBody, historyBody, modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.statusCode, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			}))
			app.modelMonitorHTTPClient = func(ModelMonitorProxyConfig) (*http.Client, error) {
				return &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
					switch request.URL.Host {
					case "status.input.im":
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(inputBody)), Request: request}, nil
					case "status.openai.com":
						return openAIClient.Transport.RoundTrip(request)
					case "status.claude.com":
						return anthropicClient.Transport.RoundTrip(request)
					case "statuspage.flashcat.cloud":
						return deepseekClient.Transport.RoundTrip(request)
					default:
						return nil, fmt.Errorf("unexpected model monitor host %q", request.URL.Host)
					}
				})}, nil
			}
			var response ModelMonitorResponse
			modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, cookies, http.StatusOK, &response)
			if len(response.Sources) != 4 {
				t.Fatalf("model monitor sources = %#v, want four sources", response.Sources)
			}
			for index, source := range response.Sources {
				if source.Groups == nil || source.Services == nil || source.Incidents == nil {
					t.Fatalf("source arrays must remain non-nil: %#v", source)
				}
				if index == 2 {
					if source.ID != "anthropic" || source.Name != "Claude Status" || source.CollectionState != test.wantState {
						t.Fatalf("unexpected Anthropic state: %#v", source)
					}
					if test.wantState == modelMonitorCollectionError {
						if source.CollectionError == nil || source.LastSuccessAt != nil || len(source.Services) != 0 || source.OverallStatus != "unknown" {
							t.Fatalf("failed Anthropic source must not retain partial success: %#v", source)
						}
					} else if source.LastSuccessAt == nil || source.OverallStatus != "degraded_performance" || len(source.Services) != 2 || len(source.Services[0].Samples) != 3 {
						t.Fatalf("successful Anthropic source lost upstream status or history: %#v", source)
					}
				} else if source.CollectionState != modelMonitorCollectionOK || source.LastSuccessAt == nil {
					t.Fatalf("Anthropic failure affected another source: %#v", source)
				}
			}
		})
	}
}

func TestModelMonitorAggregationIsolatesDeepSeekHistoryFailure(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	openAIClient := openAITestClient(t, openAITestSummary())
	anthropicClient := anthropicTestClient(t, readModelMonitorFixture(t, "anthropic_summary.json"), readModelMonitorFixture(t, "anthropic_history.html"))
	deepseekClient := deepseekFlashcatTestClient(t, []byte(`<html></html>`))
	app.modelMonitorHTTPClient = func(ModelMonitorProxyConfig) (*http.Client, error) {
		return &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Host {
			case "status.input.im":
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(readModelMonitorFixture(t, "input_im_status.json")))), Request: request}, nil
			case "status.openai.com":
				return openAIClient.Transport.RoundTrip(request)
			case "status.claude.com":
				return anthropicClient.Transport.RoundTrip(request)
			case "statuspage.flashcat.cloud":
				return deepseekClient.Transport.RoundTrip(request)
			default:
				return nil, fmt.Errorf("unexpected model monitor host %q", request.URL.Host)
			}
		})}, nil
	}

	handler := app.Routes()
	cookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{"username": "admin", "password": "test-password", "nickname": "Admin"}, nil, http.StatusOK, nil)
	var response ModelMonitorResponse
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, cookies, http.StatusOK, &response)
	if len(response.Sources) != 4 || response.Sources[0].CollectionState != modelMonitorCollectionOK || response.Sources[1].CollectionState != modelMonitorCollectionOK || response.Sources[2].CollectionState != modelMonitorCollectionOK || response.Sources[3].ID != "deepseek" || response.Sources[3].CollectionState != modelMonitorCollectionError || response.Sources[3].CollectionError == nil || !strings.Contains(*response.Sources[3].CollectionError, "DeepSeek") {
		t.Fatalf("DeepSeek history failure was not isolated: %#v", response.Sources)
	}
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
	var sourceSettings ModelMonitorSettings
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/settings", nil, adminCookies, http.StatusOK, &sourceSettings)
	if !equalStrings(sourceSettings.EnabledSourceIDs, []string{"ai-input-im", "openai", "anthropic", "deepseek"}) {
		t.Fatalf("default source settings = %#v", sourceSettings.EnabledSourceIDs)
	}
	modelMonitorRequest(t, handler, http.MethodPost, "/api/users", map[string]any{"username": "member", "password": "member-password", "nickname": "Member", "is_admin": false}, adminCookies, http.StatusOK, nil)
	memberCookies := modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/login", map[string]any{"username": "member", "password": "member-password"}, nil, http.StatusOK, nil)
	memberCookies = modelMonitorRequest(t, handler, http.MethodPost, "/api/auth/change-credentials", map[string]any{"current_password": "member-password", "password": "member-new-password"}, memberCookies, http.StatusOK, nil)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor", nil, memberCookies, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/settings", nil, memberCookies, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodPut, "/api/model-monitor/settings", map[string]any{"enabled_source_ids": []string{}}, memberCookies, http.StatusForbidden, nil)
	modelMonitorRequest(t, handler, http.MethodGet, "/api/model-monitor/sources/deepseek", nil, memberCookies, http.StatusForbidden, nil)
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
	return statuspageTestClient(t, "Anthropic", summaryBody, historyBody)
}

func anthropicShowcaseTestClient(t *testing.T, summaryBody, historyBody []byte, showcase modelMonitorRoundTripper) *http.Client {
	t.Helper()
	client := anthropicTestClient(t, summaryBody, historyBody)
	baseTransport := client.Transport
	client.Transport = modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "status.claude.com" {
			return nil, fmt.Errorf("unexpected Anthropic request: %s %s", request.Method, request.URL)
		}
		if request.URL.Path == "/uptime_showcase" {
			if request.Header.Get("Accept") != "application/json" {
				return nil, fmt.Errorf("showcase Accept = %q, want application/json", request.Header.Get("Accept"))
			}
			return showcase.RoundTrip(request)
		}
		return baseTransport.RoundTrip(request)
	})
	return client
}

func deepseekFlashcatTestClient(t *testing.T, pageBody []byte) *http.Client {
	t.Helper()
	return &http.Client{Transport: modelMonitorRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "statuspage.flashcat.cloud" || request.Host != modelMonitorDeepSeekCollectionHost || request.URL.Path != "/" {
			t.Fatalf("unexpected DeepSeek Flashcat request: url=%s host=%q", request.URL, request.Host)
		}
		if request.Header.Get("Accept") != "text/html" {
			t.Fatalf("DeepSeek Flashcat Accept = %q, want text/html", request.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(string(pageBody))),
			Request:    request,
		}, nil
	})}
}

func statuspageTestClient(t *testing.T, sourceName string, summaryBody, historyBody []byte) *http.Client {
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
			t.Fatalf("unexpected %s request path: %s", sourceName, request.URL.Path)
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
	for _, updated := range recorder.Result().Cookies() {
		replaced := false
		for index, existing := range cookies {
			if existing != nil && existing.Name == updated.Name {
				cookies[index] = updated
				replaced = true
				break
			}
		}
		if !replaced {
			cookies = append(cookies, updated)
		}
	}
	return cookies
}

func readModelMonitorFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/model_monitor/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
