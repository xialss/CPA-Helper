package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type openAISummaryPayload struct {
	Page       *openAISummaryPage   `json:"page"`
	Status     *openAISummaryStatus `json:"status"`
	Components json.RawMessage      `json:"components"`
	Incidents  json.RawMessage      `json:"incidents"`
}

type openAISummaryPage struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
}

type openAISummaryStatus struct {
	Indicator string `json:"indicator"`
}

type openAIStatusComponent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type openAISummaryIncident struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
	Shortlink string `json:"shortlink"`
	UpdatedAt string `json:"updated_at"`
}

func parseOpenAISummary(body []byte) (ModelMonitorSourceStatus, error) {
	var payload openAISummaryPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ModelMonitorSourceStatus{}, fmt.Errorf("OpenAI summary 响应不是有效 JSON: %w", err)
	}
	if payload.Page == nil || payload.Status == nil || strings.TrimSpace(payload.Page.ID) == "" || strings.TrimSpace(payload.Page.Name) == "" || strings.TrimSpace(payload.Status.Indicator) == "" {
		return ModelMonitorSourceStatus{}, errors.New("OpenAI summary 响应缺少 page 或 status 字段")
	}
	if len(payload.Components) == 0 {
		return ModelMonitorSourceStatus{}, errors.New("OpenAI summary 响应缺少 components 数组")
	}
	if len(payload.Incidents) == 0 {
		payload.Incidents = json.RawMessage("[]")
	}
	var components []openAIStatusComponent
	if err := json.Unmarshal(payload.Components, &components); err != nil || components == nil {
		return ModelMonitorSourceStatus{}, errors.New("OpenAI summary components 字段无效")
	}
	var incidents []openAISummaryIncident
	if err := json.Unmarshal(payload.Incidents, &incidents); err != nil || incidents == nil {
		return ModelMonitorSourceStatus{}, errors.New("OpenAI summary incidents 字段无效")
	}
	services := make([]ModelMonitorServiceStatus, 0, len(components))
	componentIDs := make(map[string]struct{}, len(components))
	for _, component := range components {
		if strings.TrimSpace(component.ID) == "" || strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Status) == "" {
			return ModelMonitorSourceStatus{}, errors.New("OpenAI summary component 缺少必要字段")
		}
		if _, exists := componentIDs[component.ID]; exists {
			return ModelMonitorSourceStatus{}, errors.New("OpenAI summary component ID 重复")
		}
		componentIDs[component.ID] = struct{}{}
		services = append(services, ModelMonitorServiceStatus{
			ID: component.ID, Name: component.Name, Status: normalizeModelMonitorStatus(component.Status),
			Samples: []ModelMonitorSample{},
		})
	}
	incidentItems := make([]ModelMonitorIncident, 0, len(incidents))
	incidentIDs := make(map[string]struct{}, len(incidents))
	for _, incident := range incidents {
		if strings.TrimSpace(incident.ID) == "" || strings.TrimSpace(incident.Name) == "" || strings.TrimSpace(incident.Status) == "" {
			return ModelMonitorSourceStatus{}, errors.New("OpenAI summary incident 缺少必要字段")
		}
		if _, exists := incidentIDs[incident.ID]; exists {
			return ModelMonitorSourceStatus{}, errors.New("OpenAI summary incident ID 重复")
		}
		incidentIDs[incident.ID] = struct{}{}
		incidentURL, err := parseModelMonitorIncidentURL(incident.Shortlink, "OpenAI")
		if err != nil {
			return ModelMonitorSourceStatus{}, err
		}
		incidentItems = append(incidentItems, ModelMonitorIncident{
			ID: incident.ID, Name: incident.Name, Status: incident.Status, Impact: incident.Impact,
			URL: incidentURL, UpdatedAt: parseModelMonitorTime(incident.UpdatedAt),
		})
	}
	sourceUpdatedAt := parseModelMonitorTime(payload.Page.UpdatedAt)
	if sourceUpdatedAt == nil {
		return ModelMonitorSourceStatus{}, errors.New("OpenAI summary page.updated_at 字段无效")
	}
	return ModelMonitorSourceStatus{
		OverallStatus:   normalizeModelMonitorStatus(payload.Status.Indicator),
		SourceUpdatedAt: sourceUpdatedAt,
		Groups:          []ModelMonitorServiceGroup{}, Services: services, Incidents: incidentItems,
	}, nil
}

func parseModelMonitorIncidentURL(value, sourceName string) (*string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("%s summary incident shortlink 字段无效", sourceName)
	}
	return &trimmed, nil
}

func normalizeModelMonitorStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "operational", "ok", "up":
		return "operational"
	case "minor", "degraded", "degraded_performance":
		return "degraded_performance"
	case "major", "partial_outage", "partial":
		return "partial_outage"
	case "critical", "major_outage", "full_outage", "down":
		return "major_outage"
	case "maintenance", "under_maintenance", "scheduled_maintenance":
		return "maintenance"
	default:
		return "unknown"
	}
}

func parseModelMonitorTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

type openAIPageSummary struct {
	Structure        *openAIPageStructure `json:"structure"`
	ComponentImpacts []openAIPageImpact   `json:"component_impacts"`
	ComponentUptimes []openAIPageUptime   `json:"component_uptimes"`
	IncidentLinks    []openAIIncidentLink `json:"incident_links"`
}

type openAIIncidentLink struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type openAIPageStructure struct {
	Items []openAIPageStructureItem `json:"items"`
}

type openAIPageStructureItem struct {
	Group *openAIPageGroup `json:"group"`
}

type openAIPageGroup struct {
	ID                      string                     `json:"id"`
	Name                    string                     `json:"name"`
	DisplayAggregatedUptime *bool                      `json:"display_aggregated_uptime"`
	Hidden                  *bool                      `json:"hidden"`
	Components              []openAIPageGroupComponent `json:"components"`
}

type openAIPageGroupComponent struct {
	ComponentID        string `json:"component_id"`
	Name               string `json:"name"`
	DataAvailableSince string `json:"data_available_since"`
	DisplayUptime      *bool  `json:"display_uptime"`
	Hidden             *bool  `json:"hidden"`
}

type openAIPageImpact struct {
	ComponentID string  `json:"component_id"`
	StartAt     string  `json:"start_at"`
	EndAt       *string `json:"end_at"`
	Status      string  `json:"status"`
	IncidentID  string  `json:"status_page_incident_id"`
}

type openAIPageUptime struct {
	ComponentID                string `json:"component_id"`
	DataAvailableSince         string `json:"data_available_since"`
	StatusPageComponentGroupID string `json:"status_page_component_group_id"`
	Uptime                     string `json:"uptime"`
}

type openAIInitialNow struct {
	ISODate string `json:"isoDate"`
}

type openAIComponentsPayload struct {
	Components json.RawMessage `json:"components"`
}

func collectOpenAIStatus(ctx context.Context, client *http.Client, baseURL string) (ModelMonitorSourceStatus, error) {
	currentBody, err := modelMonitorGet(ctx, client, strings.TrimRight(baseURL, "/")+"/api/v2/summary.json", "application/json")
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	current, err := parseOpenAISummary(currentBody)
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	summaryServices := current.Services
	componentsBody, err := modelMonitorGet(ctx, client, strings.TrimRight(baseURL, "/")+"/api/v2/components.json", "application/json")
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	completeServices, err := parseOpenAIComponents(componentsBody)
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	if err := validateOpenAISummaryComponents(summaryServices, completeServices); err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	current.Services = completeServices
	pageBody, err := modelMonitorGet(ctx, client, strings.TrimRight(baseURL, "/")+"/", "text/html")
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	summary, now, err := parseOpenAIPageHistory(pageBody)
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	if err := applyOpenAIHistory(&current, summary, now); err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	return current, nil
}

func parseOpenAIComponents(body []byte) ([]ModelMonitorServiceStatus, error) {
	var payload openAIComponentsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("OpenAI components 响应不是有效 JSON: %w", err)
	}
	if len(payload.Components) == 0 {
		return nil, errors.New("OpenAI components 响应缺少 components 数组")
	}
	var components []openAIStatusComponent
	if err := json.Unmarshal(payload.Components, &components); err != nil || components == nil || len(components) == 0 {
		return nil, errors.New("OpenAI components 字段无效")
	}
	services := make([]ModelMonitorServiceStatus, 0, len(components))
	ids := make(map[string]struct{}, len(components))
	for _, component := range components {
		if strings.TrimSpace(component.ID) == "" || strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Status) == "" {
			return nil, errors.New("OpenAI component 缺少必要字段")
		}
		if _, exists := ids[component.ID]; exists {
			return nil, fmt.Errorf("OpenAI component ID %q 重复", component.ID)
		}
		ids[component.ID] = struct{}{}
		services = append(services, ModelMonitorServiceStatus{
			ID: component.ID, Name: component.Name, Status: normalizeModelMonitorStatus(component.Status),
			Samples: []ModelMonitorSample{},
		})
	}
	return services, nil
}

func validateOpenAISummaryComponents(summary, complete []ModelMonitorServiceStatus) error {
	completeIDs := make(map[string]struct{}, len(complete))
	for _, service := range complete {
		completeIDs[service.ID] = struct{}{}
	}
	for _, service := range summary {
		if _, exists := completeIDs[service.ID]; !exists {
			return fmt.Errorf("OpenAI summary 引用了 components 响应中不存在的组件 %q", service.ID)
		}
	}
	return nil
}

func parseOpenAIPageHistory(body []byte) (openAIPageSummary, time.Time, error) {
	text := string(body)
	const prefix = "self.__next_f.push("
	var currentMatches, historyMatches int
	var selectedStructure *openAIPageStructure
	var selectedHistory openAIPageSummary
	var selectedNow time.Time
	for offset := 0; ; {
		start := strings.Index(text[offset:], prefix)
		if start < 0 {
			break
		}
		start += offset + len(prefix)
		end := strings.Index(text[start:], ")</script>")
		if end < 0 {
			break
		}
		end += start
		var frame []json.RawMessage
		if json.Unmarshal([]byte(text[start:end]), &frame) == nil && len(frame) >= 2 {
			var decoded string
			if json.Unmarshal(frame[1], &decoded) == nil {
				if strings.Contains(decoded, `"initialNow"`) && strings.Contains(decoded, `"structure"`) {
					summaryJSON, summaryErr := extractJSONObjectAfterKey(decoded, "summary")
					nowJSON, nowErr := extractJSONObjectAfterKey(decoded, "initialNow")
					var summary openAIPageSummary
					var initialNow openAIInitialNow
					if summaryErr == nil && nowErr == nil && json.Unmarshal(summaryJSON, &summary) == nil && json.Unmarshal(nowJSON, &initialNow) == nil {
						parsedNow, parseErr := time.Parse(time.RFC3339Nano, initialNow.ISODate)
						if parseErr == nil && summary.Structure != nil && summary.Structure.Items != nil {
							currentMatches++
							selectedStructure = summary.Structure
							selectedNow = parsedNow.UTC()
						}
					}
				}
				if strings.Contains(decoded, `"component_impacts"`) && strings.Contains(decoded, `"component_uptimes"`) && strings.Contains(decoded, `"incident_links"`) {
					for _, key := range []string{"data", "summary"} {
						historyJSON, historyErr := extractJSONObjectAfterKey(decoded, key)
						var history openAIPageSummary
						if historyErr == nil && json.Unmarshal(historyJSON, &history) == nil && history.ComponentImpacts != nil && history.ComponentUptimes != nil && history.IncidentLinks != nil {
							historyMatches++
							selectedHistory = history
							break
						}
					}
				}
			}
		}
		offset = end + len(")</script>")
	}
	if currentMatches != 1 || historyMatches != 1 {
		return openAIPageSummary{}, time.Time{}, fmt.Errorf("OpenAI 页面当前结构数量为 %d、历史结构数量为 %d，均期望 1", currentMatches, historyMatches)
	}
	selectedHistory.Structure = selectedStructure
	return selectedHistory, selectedNow, nil
}

func extractJSONObjectAfterKey(text, key string) ([]byte, error) {
	marker := `"` + key + `":`
	index := strings.Index(text, marker)
	if index < 0 {
		return nil, fmt.Errorf("缺少 %s 字段", key)
	}
	index += len(marker)
	for index < len(text) && (text[index] == ' ' || text[index] == '\n' || text[index] == '\r' || text[index] == '\t') {
		index++
	}
	if index >= len(text) || text[index] != '{' {
		return nil, fmt.Errorf("%s 字段不是对象", key)
	}
	depth := 0
	inString := false
	escaped := false
	for cursor := index; cursor < len(text); cursor++ {
		char := text[cursor]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(text[index : cursor+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("%s 对象未闭合", key)
}

type parsedOpenAIPageImpact struct {
	start         time.Time
	end           time.Time
	status        string
	incidentTitle string
}

type parsedOpenAIPageUptime struct {
	percent        *float64
	availableSince time.Time
}

func applyOpenAIHistory(current *ModelMonitorSourceStatus, summary openAIPageSummary, now time.Time) error {
	if summary.Structure == nil || summary.Structure.Items == nil || len(summary.Structure.Items) == 0 || now.IsZero() {
		return errors.New("OpenAI 页面分组结构无效")
	}
	if summary.IncidentLinks == nil {
		return errors.New("OpenAI 页面缺少 incident links")
	}
	currentByID := make(map[string]ModelMonitorServiceStatus, len(current.Services))
	for _, service := range current.Services {
		if strings.TrimSpace(service.ID) == "" || strings.TrimSpace(service.Name) == "" || strings.TrimSpace(service.Status) == "" {
			return errors.New("OpenAI 当前组件缺少必要字段")
		}
		if _, exists := currentByID[service.ID]; exists {
			return fmt.Errorf("OpenAI 当前组件 ID %q 重复", service.ID)
		}
		currentByID[service.ID] = service
	}
	if len(currentByID) == 0 {
		return errors.New("OpenAI 当前组件为空")
	}
	incidentTitleByID := make(map[string]string, len(summary.IncidentLinks))
	for _, incident := range summary.IncidentLinks {
		if strings.TrimSpace(incident.ID) == "" || strings.TrimSpace(incident.Name) == "" {
			return errors.New("OpenAI 页面 incident link 缺少 ID 或标题")
		}
		if _, exists := incidentTitleByID[incident.ID]; exists {
			return fmt.Errorf("OpenAI 页面 incident link ID %q 重复", incident.ID)
		}
		incidentTitleByID[incident.ID] = incident.Name
	}

	groupsByID := make(map[string]*openAIPageGroup, len(summary.Structure.Items))
	componentMetadata := make(map[string]openAIPageGroupComponent, len(currentByID))
	for index := range summary.Structure.Items {
		group := summary.Structure.Items[index].Group
		if group == nil || strings.TrimSpace(group.ID) == "" || strings.TrimSpace(group.Name) == "" || group.Hidden == nil || group.DisplayAggregatedUptime == nil || group.Components == nil {
			return errors.New("OpenAI 页面 group 缺少必要字段")
		}
		if _, exists := groupsByID[group.ID]; exists {
			return fmt.Errorf("OpenAI 页面 group ID %q 重复", group.ID)
		}
		groupsByID[group.ID] = group
		for _, component := range group.Components {
			if strings.TrimSpace(component.ComponentID) == "" || strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.DataAvailableSince) == "" || component.Hidden == nil || component.DisplayUptime == nil {
				return fmt.Errorf("OpenAI group %q 的 component 缺少必要字段", group.ID)
			}
			if _, exists := componentMetadata[component.ComponentID]; exists {
				return fmt.Errorf("OpenAI 页面 component ID %q 重复", component.ComponentID)
			}
			if _, exists := currentByID[component.ComponentID]; !exists {
				return fmt.Errorf("OpenAI 可见结构引用的 component %q 缺少当前状态", component.ComponentID)
			}
			if _, err := time.Parse(time.RFC3339Nano, component.DataAvailableSince); err != nil {
				return fmt.Errorf("OpenAI component %q 的 data_available_since 无效", component.ComponentID)
			}
			componentMetadata[component.ComponentID] = component
		}
	}
	for componentID := range currentByID {
		if _, exists := componentMetadata[componentID]; !exists {
			return fmt.Errorf("OpenAI 当前 component %q 未出现在页面分组中", componentID)
		}
	}

	uptimeByComponent := make(map[string]parsedOpenAIPageUptime, len(summary.ComponentUptimes))
	uptimeByGroup := make(map[string]parsedOpenAIPageUptime, len(groupsByID))
	for _, item := range summary.ComponentUptimes {
		if item.ComponentID == "$undefined" && strings.TrimSpace(item.StatusPageComponentGroupID) != "" {
			if _, exists := groupsByID[item.StatusPageComponentGroupID]; !exists {
				return fmt.Errorf("OpenAI uptime 引用了未知 group %q", item.StatusPageComponentGroupID)
			}
			if _, exists := uptimeByGroup[item.StatusPageComponentGroupID]; exists {
				return fmt.Errorf("OpenAI group %q 的 uptime 重复", item.StatusPageComponentGroupID)
			}
			uptime, err := parseOpenAIUptime(item, "group "+item.StatusPageComponentGroupID)
			if err != nil {
				return err
			}
			uptimeByGroup[item.StatusPageComponentGroupID] = uptime
			continue
		}
		if strings.TrimSpace(item.StatusPageComponentGroupID) != "" && item.StatusPageComponentGroupID != "$undefined" {
			return fmt.Errorf("OpenAI component %q 的 uptime group 引用无效", item.ComponentID)
		}
		if _, exists := componentMetadata[item.ComponentID]; !exists {
			return fmt.Errorf("OpenAI uptime 引用了未知组件 %q", item.ComponentID)
		}
		if _, exists := uptimeByComponent[item.ComponentID]; exists {
			return fmt.Errorf("OpenAI 组件 %q 的 uptime 重复", item.ComponentID)
		}
		uptime, err := parseOpenAIUptime(item, "组件 "+item.ComponentID)
		if err != nil {
			return err
		}
		uptimeByComponent[item.ComponentID] = uptime
	}
	for groupID, group := range groupsByID {
		if *group.DisplayAggregatedUptime {
			if _, exists := uptimeByGroup[groupID]; !exists {
				return fmt.Errorf("OpenAI group %q 缺少 aggregated uptime", groupID)
			}
		}
	}
	impactsByComponent := make(map[string][]parsedOpenAIPageImpact)
	for _, impact := range summary.ComponentImpacts {
		if _, exists := componentMetadata[impact.ComponentID]; !exists {
			return fmt.Errorf("OpenAI impact 引用了未知组件 %q", impact.ComponentID)
		}
		start, err := time.Parse(time.RFC3339Nano, impact.StartAt)
		if err != nil || strings.TrimSpace(impact.Status) == "" {
			return fmt.Errorf("OpenAI 组件 %q 的 impact 结构无效", impact.ComponentID)
		}
		end := now
		if impact.EndAt != nil && *impact.EndAt != "$undefined" {
			end, err = time.Parse(time.RFC3339Nano, *impact.EndAt)
			if err != nil || end.Before(start) {
				return fmt.Errorf("OpenAI 组件 %q 的 impact 时间范围无效", impact.ComponentID)
			}
		}
		incidentTitle := ""
		if impact.IncidentID != "" {
			var exists bool
			incidentTitle, exists = incidentTitleByID[impact.IncidentID]
			if !exists {
				return fmt.Errorf("OpenAI impact 引用了无法映射的 incident %q", impact.IncidentID)
			}
		}
		impactsByComponent[impact.ComponentID] = append(impactsByComponent[impact.ComponentID], parsedOpenAIPageImpact{
			start: start, end: end, status: normalizeModelMonitorStatus(impact.Status), incidentTitle: incidentTitle,
		})
	}

	groups := make([]ModelMonitorServiceGroup, 0, len(summary.Structure.Items))
	for _, item := range summary.Structure.Items {
		group := item.Group
		if *group.Hidden {
			continue
		}
		services := make([]ModelMonitorServiceStatus, 0, len(group.Components))
		groupStatus := "operational"
		for _, metadata := range group.Components {
			if *metadata.Hidden {
				continue
			}
			service := currentByID[metadata.ComponentID]
			service.Name = metadata.Name
			uptime, hasUptime := uptimeByComponent[metadata.ComponentID]
			if *metadata.DisplayUptime && !hasUptime {
				return fmt.Errorf("OpenAI component %q 缺少 uptime", metadata.ComponentID)
			}
			if *metadata.DisplayUptime {
				service.UptimePercent = uptime.percent
			} else {
				service.UptimePercent = nil
			}
			availableSince, _ := time.Parse(time.RFC3339Nano, metadata.DataAvailableSince)
			service.Samples = projectOpenAIComponentSamples(now, availableSince.UTC(), impactsByComponent[metadata.ComponentID])
			services = append(services, service)
			groupStatus = worseModelMonitorStatus(groupStatus, service.Status)
		}
		if len(services) == 0 {
			return fmt.Errorf("OpenAI 可见 group %q 没有可见 component", group.ID)
		}
		result := ModelMonitorServiceGroup{
			ID: group.ID, Name: group.Name, Status: groupStatus,
			Samples: []ModelMonitorSample{}, Services: services,
		}
		if *group.DisplayAggregatedUptime {
			uptime := uptimeByGroup[group.ID]
			result.UptimePercent = uptime.percent
			result.Samples = aggregateOpenAIGroupSamples(services, uptime.availableSince)
		}
		groups = append(groups, result)
	}
	current.Groups = groups
	current.Services = []ModelMonitorServiceStatus{}
	return nil
}

func parseOpenAIUptime(item openAIPageUptime, label string) (parsedOpenAIPageUptime, error) {
	value, err := strconv.ParseFloat(item.Uptime, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		return parsedOpenAIPageUptime{}, fmt.Errorf("OpenAI %s 的 uptime 无效", label)
	}
	availableSince, err := time.Parse(time.RFC3339Nano, item.DataAvailableSince)
	if err != nil {
		return parsedOpenAIPageUptime{}, fmt.Errorf("OpenAI %s 的 data_available_since 无效", label)
	}
	copy := value
	return parsedOpenAIPageUptime{percent: &copy, availableSince: availableSince.UTC()}, nil
}

func projectOpenAIComponentSamples(now, availableSince time.Time, impacts []parsedOpenAIPageImpact) []ModelMonitorSample {
	windowEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := windowEnd.AddDate(0, 0, -89)
	componentWindowStart := time.Date(availableSince.Year(), availableSince.Month(), availableSince.Day(), 0, 0, 0, 0, time.UTC)
	if componentWindowStart.Before(windowStart) {
		componentWindowStart = windowStart
	}
	samples := make([]ModelMonitorSample, 0, 90)
	for day := componentWindowStart; !day.After(windowEnd); day = day.AddDate(0, 0, 1) {
		status := "operational"
		related := make([]string, 0)
		for _, impact := range impacts {
			if impact.start.Before(day.AddDate(0, 0, 1)) && impact.end.After(day) {
				status = worseModelMonitorStatus(status, impact.status)
				if impact.incidentTitle != "" && !containsString(related, impact.incidentTitle) {
					related = append(related, impact.incidentTitle)
				}
			}
		}
		samples = append(samples, ModelMonitorSample{Timestamp: day, Status: status, RelatedIncidents: related})
	}
	return samples
}

func aggregateOpenAIGroupSamples(services []ModelMonitorServiceStatus, availableSince time.Time) []ModelMonitorSample {
	windowStart := time.Date(availableSince.Year(), availableSince.Month(), availableSince.Day(), 0, 0, 0, 0, time.UTC)
	byDay := make(map[time.Time]ModelMonitorSample)
	for _, service := range services {
		for _, sample := range service.Samples {
			if sample.Timestamp.Before(windowStart) {
				continue
			}
			combined, exists := byDay[sample.Timestamp]
			if !exists {
				combined = ModelMonitorSample{Timestamp: sample.Timestamp, Status: "operational", RelatedIncidents: []string{}}
			}
			combined.Status = worseModelMonitorStatus(combined.Status, sample.Status)
			for _, incidentID := range sample.RelatedIncidents {
				if !containsString(combined.RelatedIncidents, incidentID) {
					combined.RelatedIncidents = append(combined.RelatedIncidents, incidentID)
				}
			}
			byDay[sample.Timestamp] = combined
		}
	}
	samples := make([]ModelMonitorSample, 0, len(byDay))
	for _, sample := range byDay {
		samples = append(samples, sample)
	}
	sort.Slice(samples, func(left, right int) bool { return samples[left].Timestamp.Before(samples[right].Timestamp) })
	return samples
}

func worseModelMonitorStatus(left, right string) string {
	severity := map[string]int{
		"operational": 1, "unknown": 2, "maintenance": 3,
		"degraded_performance": 4, "partial_outage": 5, "major_outage": 6,
	}
	if severity[right] > severity[left] {
		return right
	}
	return left
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type anthropicSummaryPayload struct {
	Page       *openAISummaryPage   `json:"page"`
	Status     *openAISummaryStatus `json:"status"`
	Components json.RawMessage      `json:"components"`
	Incidents  json.RawMessage      `json:"incidents"`
}

type anthropicUptimeComponent struct {
	Component *anthropicUptimeComponentMetadata `json:"component"`
	Days      json.RawMessage                   `json:"days"`
}

type anthropicUptimeComponentMetadata struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type anthropicUptimeDay struct {
	Date          string          `json:"date"`
	Outages       json.RawMessage `json:"outages"`
	RelatedEvents json.RawMessage `json:"related_events"`
}

type anthropicRelatedEvent struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

var anthropicUptimeAssignmentPattern = regexp.MustCompile(`\bwindow\s*\.\s*uptimeData\s*=`)

func collectAnthropicStatus(ctx context.Context, client *http.Client, baseURL string) (ModelMonitorSourceStatus, error) {
	summaryBody, err := modelMonitorGet(ctx, client, strings.TrimRight(baseURL, "/")+"/api/v2/summary.json", "application/json")
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	current, err := parseAnthropicSummary(summaryBody)
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	historyBody, err := modelMonitorGet(ctx, client, strings.TrimRight(baseURL, "/")+"/", "text/html")
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	services, err := parseAnthropicPageHistory(historyBody, current.Services)
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	current.Services = services
	return current, nil
}

func parseAnthropicSummary(body []byte) (ModelMonitorSourceStatus, error) {
	var payload anthropicSummaryPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ModelMonitorSourceStatus{}, fmt.Errorf("Anthropic summary 响应不是有效 JSON: %w", err)
	}
	if payload.Page == nil || payload.Status == nil || strings.TrimSpace(payload.Page.ID) == "" || strings.TrimSpace(payload.Page.Name) == "" || strings.TrimSpace(payload.Status.Indicator) == "" {
		return ModelMonitorSourceStatus{}, errors.New("Anthropic summary 响应缺少 page 或 status 字段")
	}
	var components []openAIStatusComponent
	if len(payload.Components) == 0 || json.Unmarshal(payload.Components, &components) != nil || len(components) == 0 {
		return ModelMonitorSourceStatus{}, errors.New("Anthropic summary components 字段无效")
	}
	var incidents []openAISummaryIncident
	if len(payload.Incidents) == 0 || json.Unmarshal(payload.Incidents, &incidents) != nil || incidents == nil {
		return ModelMonitorSourceStatus{}, errors.New("Anthropic summary incidents 字段无效")
	}

	services := make([]ModelMonitorServiceStatus, 0, len(components))
	componentIDs := make(map[string]struct{}, len(components))
	for _, component := range components {
		if strings.TrimSpace(component.ID) == "" || strings.TrimSpace(component.Name) == "" || strings.TrimSpace(component.Status) == "" {
			return ModelMonitorSourceStatus{}, errors.New("Anthropic summary component 缺少必要字段")
		}
		if _, exists := componentIDs[component.ID]; exists {
			return ModelMonitorSourceStatus{}, fmt.Errorf("Anthropic summary component ID %q 重复", component.ID)
		}
		componentIDs[component.ID] = struct{}{}
		services = append(services, ModelMonitorServiceStatus{
			ID: component.ID, Name: component.Name, Status: normalizeModelMonitorStatus(component.Status),
			Samples: []ModelMonitorSample{},
		})
	}

	incidentItems := make([]ModelMonitorIncident, 0, len(incidents))
	incidentIDs := make(map[string]struct{}, len(incidents))
	for _, incident := range incidents {
		if strings.TrimSpace(incident.ID) == "" || strings.TrimSpace(incident.Name) == "" || strings.TrimSpace(incident.Status) == "" {
			return ModelMonitorSourceStatus{}, errors.New("Anthropic summary incident 缺少必要字段")
		}
		if _, exists := incidentIDs[incident.ID]; exists {
			return ModelMonitorSourceStatus{}, fmt.Errorf("Anthropic summary incident ID %q 重复", incident.ID)
		}
		incidentIDs[incident.ID] = struct{}{}
		incidentURL, err := parseModelMonitorIncidentURL(incident.Shortlink, "Anthropic")
		if err != nil {
			return ModelMonitorSourceStatus{}, err
		}
		var updatedAt *time.Time
		if strings.TrimSpace(incident.UpdatedAt) != "" {
			updatedAt = parseModelMonitorTime(incident.UpdatedAt)
			if updatedAt == nil {
				return ModelMonitorSourceStatus{}, errors.New("Anthropic summary incident updated_at 字段无效")
			}
		}
		incidentItems = append(incidentItems, ModelMonitorIncident{
			ID: incident.ID, Name: incident.Name, Status: incident.Status, Impact: incident.Impact,
			URL: incidentURL, UpdatedAt: updatedAt,
		})
	}

	sourceUpdatedAt := parseModelMonitorTime(payload.Page.UpdatedAt)
	if sourceUpdatedAt == nil {
		return ModelMonitorSourceStatus{}, errors.New("Anthropic summary page.updated_at 字段无效")
	}
	return ModelMonitorSourceStatus{
		OverallStatus: normalizeModelMonitorStatus(payload.Status.Indicator), SourceUpdatedAt: sourceUpdatedAt,
		Groups: []ModelMonitorServiceGroup{}, Services: services, Incidents: incidentItems,
	}, nil
}

func parseAnthropicPageHistory(body []byte, currentServices []ModelMonitorServiceStatus) ([]ModelMonitorServiceStatus, error) {
	matches := anthropicUptimeAssignmentPattern.FindAllIndex(body, -1)
	if len(matches) != 1 {
		return nil, fmt.Errorf("Anthropic 页面 uptimeData 赋值数量为 %d，期望 1", len(matches))
	}
	objectStart := matches[0][1]
	for objectStart < len(body) && (body[objectStart] == ' ' || body[objectStart] == '\n' || body[objectStart] == '\r' || body[objectStart] == '\t') {
		objectStart++
	}
	object, err := extractBalancedJSONObjectAt(body, objectStart)
	if err != nil {
		return nil, fmt.Errorf("Anthropic 页面 uptimeData 无效: %w", err)
	}
	assignmentEnd := objectStart + len(object)
	for assignmentEnd < len(body) && (body[assignmentEnd] == ' ' || body[assignmentEnd] == '\n' || body[assignmentEnd] == '\r' || body[assignmentEnd] == '\t') {
		assignmentEnd++
	}
	if assignmentEnd >= len(body) || body[assignmentEnd] != ';' {
		return nil, errors.New("Anthropic 页面 uptimeData 赋值尾部无效")
	}
	rawComponents, err := decodeAnthropicUptimeComponents(object)
	if err != nil {
		return nil, err
	}

	currentByID := make(map[string]ModelMonitorServiceStatus, len(currentServices))
	for _, service := range currentServices {
		if strings.TrimSpace(service.ID) == "" || strings.TrimSpace(service.Name) == "" {
			return nil, errors.New("Anthropic 当前组件缺少必要字段")
		}
		if _, exists := currentByID[service.ID]; exists {
			return nil, fmt.Errorf("Anthropic 当前组件 ID %q 重复", service.ID)
		}
		currentByID[service.ID] = service
	}
	if len(currentByID) == 0 {
		return nil, errors.New("Anthropic 当前组件为空")
	}

	historyByID := make(map[string][]ModelMonitorSample, len(rawComponents))
	for componentID, rawComponent := range rawComponents {
		current, exists := currentByID[componentID]
		if !exists {
			return nil, fmt.Errorf("Anthropic 历史包含未知组件 %q", componentID)
		}
		var component anthropicUptimeComponent
		if err := json.Unmarshal(rawComponent, &component); err != nil || component.Component == nil {
			return nil, fmt.Errorf("Anthropic 组件 %q 的历史结构无效", componentID)
		}
		if strings.TrimSpace(component.Component.Code) == "" || strings.TrimSpace(component.Component.Name) == "" || component.Component.Code != componentID || component.Component.Name != current.Name {
			return nil, fmt.Errorf("Anthropic 组件 %q 的历史元数据不一致", componentID)
		}
		var days []anthropicUptimeDay
		if len(component.Days) == 0 || json.Unmarshal(component.Days, &days) != nil || len(days) == 0 {
			return nil, fmt.Errorf("Anthropic 组件 %q 的 days 字段无效", componentID)
		}
		samples, err := parseAnthropicUptimeDays(componentID, days)
		if err != nil {
			return nil, err
		}
		historyByID[componentID] = samples
	}

	services := make([]ModelMonitorServiceStatus, 0, len(currentServices))
	for _, current := range currentServices {
		samples, exists := historyByID[current.ID]
		if !exists {
			return nil, fmt.Errorf("Anthropic 当前组件 %q 缺少历史", current.ID)
		}
		current.UptimePercent = nil
		current.Samples = samples
		services = append(services, current)
	}
	return services, nil
}

func decodeAnthropicUptimeComponents(object []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("Anthropic 页面 uptimeData 不是有效的非空对象")
	}

	components := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("Anthropic 页面 uptimeData 不是有效的非空对象")
		}
		componentID, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("Anthropic 页面 uptimeData 不是有效的非空对象")
		}
		if _, exists := components[componentID]; exists {
			return nil, fmt.Errorf("Anthropic 页面 uptimeData 组件 key %q 重复", componentID)
		}
		var rawComponent json.RawMessage
		if err := decoder.Decode(&rawComponent); err != nil {
			return nil, errors.New("Anthropic 页面 uptimeData 不是有效的非空对象")
		}
		components[componentID] = rawComponent
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(components) == 0 {
		return nil, errors.New("Anthropic 页面 uptimeData 不是有效的非空对象")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("Anthropic 页面 uptimeData 不是有效的非空对象")
	}
	return components, nil
}

func extractBalancedJSONObjectAt(body []byte, start int) ([]byte, error) {
	if start >= len(body) || body[start] != '{' {
		return nil, errors.New("赋值右侧不是 JSON 对象")
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(body); index++ {
		char := body[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : index+1], nil
			}
		}
	}
	return nil, errors.New("JSON 对象未闭合")
}

func parseAnthropicUptimeDays(componentID string, days []anthropicUptimeDay) ([]ModelMonitorSample, error) {
	samples := make([]ModelMonitorSample, 0, len(days))
	dates := make(map[string]struct{}, len(days))
	for _, day := range days {
		parsedDate, err := time.Parse("2006-01-02", day.Date)
		if err != nil || parsedDate.Format("2006-01-02") != day.Date {
			return nil, fmt.Errorf("Anthropic 组件 %q 包含无效日期", componentID)
		}
		if _, exists := dates[day.Date]; exists {
			return nil, fmt.Errorf("Anthropic 组件 %q 的日期 %q 重复", componentID, day.Date)
		}
		dates[day.Date] = struct{}{}

		var outages map[string]json.RawMessage
		if len(day.Outages) == 0 || json.Unmarshal(day.Outages, &outages) != nil || outages == nil {
			return nil, fmt.Errorf("Anthropic 组件 %q 的 outages 字段无效", componentID)
		}
		status := "operational"
		for outageType, rawDuration := range outages {
			var duration *float64
			if (outageType != "m" && outageType != "p") || json.Unmarshal(rawDuration, &duration) != nil || duration == nil || *duration < 0 {
				return nil, fmt.Errorf("Anthropic 组件 %q 的 outage %q 无效", componentID, outageType)
			}
			if outageType == "m" {
				status = worseModelMonitorStatus(status, "major_outage")
			} else {
				status = worseModelMonitorStatus(status, "partial_outage")
			}
		}

		var events []anthropicRelatedEvent
		if len(day.RelatedEvents) == 0 || json.Unmarshal(day.RelatedEvents, &events) != nil || events == nil {
			return nil, fmt.Errorf("Anthropic 组件 %q 的 related_events 字段无效", componentID)
		}
		related := make([]string, 0, len(events))
		seenCodes := make(map[string]struct{}, len(events))
		seenNames := make(map[string]struct{}, len(events))
		for _, event := range events {
			code := strings.TrimSpace(event.Code)
			name := strings.TrimSpace(event.Name)
			if code == "" || name == "" {
				return nil, fmt.Errorf("Anthropic 组件 %q 的 related event 缺少名称或 code", componentID)
			}
			if _, exists := seenCodes[code]; exists {
				continue
			}
			seenCodes[code] = struct{}{}
			if _, exists := seenNames[name]; exists {
				continue
			}
			seenNames[name] = struct{}{}
			related = append(related, name)
		}
		samples = append(samples, ModelMonitorSample{
			Timestamp: parsedDate.UTC(), Status: status, RelatedIncidents: related,
		})
	}
	sort.Slice(samples, func(left, right int) bool { return samples[left].Timestamp.Before(samples[right].Timestamp) })
	if len(samples) > 90 {
		samples = samples[len(samples)-90:]
	}
	return samples, nil
}

type inputIMPayload struct {
	AllOK       *bool            `json:"all_ok"`
	GeneratedAt int64            `json:"generated_at"`
	Services    []inputIMService `json:"services"`
}

type inputIMService struct {
	Model     string          `json:"model"`
	UptimePct *float64        `json:"uptime_pct"`
	Last      *inputIMSample  `json:"last"`
	History   []inputIMSample `json:"history"`
}

type inputIMSample struct {
	Timestamp int64    `json:"ts"`
	OK        *bool    `json:"ok"`
	LatencyMS *float64 `json:"latency_ms"`
	Error     *string  `json:"error"`
}

type parsedInputIMSample struct {
	sample    inputIMSample
	timestamp time.Time
}

func parseInputIMTimestamp(value int64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	timestamp := time.Unix(value, 0).UTC()
	if _, err := timestamp.MarshalJSON(); err != nil {
		return time.Time{}, false
	}
	return timestamp, true
}

func collectInputIMStatus(ctx context.Context, client *http.Client, baseURL string) (ModelMonitorSourceStatus, error) {
	body, err := modelMonitorGet(ctx, client, strings.TrimRight(baseURL, "/")+"/api/status", "application/json")
	if err != nil {
		return ModelMonitorSourceStatus{}, err
	}
	return parseInputIMStatus(body)
}

func parseInputIMStatus(body []byte) (ModelMonitorSourceStatus, error) {
	var payload inputIMPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ModelMonitorSourceStatus{}, fmt.Errorf("AI.INPUT.IM 响应不是有效 JSON: %w", err)
	}
	generatedAt, generatedAtValid := parseInputIMTimestamp(payload.GeneratedAt)
	if payload.AllOK == nil || !generatedAtValid || payload.Services == nil || len(payload.Services) == 0 {
		return ModelMonitorSourceStatus{}, errors.New("AI.INPUT.IM 响应缺少必要字段")
	}
	services := make([]ModelMonitorServiceStatus, 0, len(payload.Services))
	serviceIDs := make(map[string]struct{}, len(payload.Services))
	failing := 0
	for _, service := range payload.Services {
		lastTimestampValid := false
		if service.Last != nil {
			_, lastTimestampValid = parseInputIMTimestamp(service.Last.Timestamp)
		}
		if strings.TrimSpace(service.Model) == "" || service.UptimePct == nil || *service.UptimePct < 0 || *service.UptimePct > 100 || service.Last == nil || service.Last.OK == nil || !lastTimestampValid || (service.Last.LatencyMS != nil && *service.Last.LatencyMS < 0) || service.History == nil || len(service.History) == 0 {
			return ModelMonitorSourceStatus{}, errors.New("AI.INPUT.IM service 结构无效")
		}
		if _, exists := serviceIDs[service.Model]; exists {
			return ModelMonitorSourceStatus{}, errors.New("AI.INPUT.IM service model 重复")
		}
		serviceIDs[service.Model] = struct{}{}
		history := make([]parsedInputIMSample, 0, len(service.History))
		for _, sample := range service.History {
			timestamp, timestampValid := parseInputIMTimestamp(sample.Timestamp)
			if sample.OK == nil || !timestampValid || (sample.LatencyMS != nil && *sample.LatencyMS < 0) {
				return ModelMonitorSourceStatus{}, errors.New("AI.INPUT.IM 分钟样本结构无效")
			}
			history = append(history, parsedInputIMSample{sample: sample, timestamp: timestamp})
		}
		sort.SliceStable(history, func(left, right int) bool { return history[left].timestamp.Before(history[right].timestamp) })
		if len(history) > 60 {
			history = history[len(history)-60:]
		}
		samples := make([]ModelMonitorSample, 0, len(history))
		for _, parsedSample := range history {
			sample := parsedSample.sample
			status := "major_outage"
			if *sample.OK {
				status = "operational"
			}
			samples = append(samples, ModelMonitorSample{
				Timestamp: parsedSample.timestamp, Status: status,
				LatencyMS: sample.LatencyMS, Error: sample.Error, RelatedIncidents: []string{},
			})
		}
		lastStatus := "major_outage"
		if *service.Last.OK {
			lastStatus = "operational"
		} else {
			failing++
		}
		services = append(services, ModelMonitorServiceStatus{
			ID: service.Model, Name: service.Model, Status: lastStatus,
			UptimePercent: service.UptimePct, LastStatus: &lastStatus,
			LastLatencyMS: service.Last.LatencyMS, LastError: service.Last.Error, Samples: samples,
		})
	}
	overall := "operational"
	if !*payload.AllOK {
		overall = "partial_outage"
		if failing == len(services) {
			overall = "major_outage"
		}
	}
	return ModelMonitorSourceStatus{
		OverallStatus: overall, SourceUpdatedAt: &generatedAt,
		Groups: []ModelMonitorServiceGroup{}, Services: services, Incidents: []ModelMonitorIncident{},
	}, nil
}
