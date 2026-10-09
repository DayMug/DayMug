package storetest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

var (
	_ store.UsageEventRecorder  = (*Fake)(nil)
	_ store.ClaudeUsageRecorder = (*Fake)(nil)
)

// RecordUsageEvent applies the same defaults as the SQLite insert and, like its
// INSERT OR IGNORE, keeps the first row written under a given id.
func (m *Fake) RecordUsageEvent(_ context.Context, event store.UsageEvent) error {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.EventID) == "" {
		return fmt.Errorf("usage event: id and event_id required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	event.OccurredAt = event.OccurredAt.UTC()
	if event.Day == "" {
		event.Day = event.OccurredAt.Format("2006-01-02")
	}
	if strings.TrimSpace(event.Model) == "" {
		event.Model = "unknown"
	}
	if event.SourceType == "" {
		event.SourceType = "manual"
	}
	if event.RequestCountScope == "" {
		event.RequestCountScope = "billing_event"
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, existing := range m.UsageEvents {
		if existing.ID == event.ID {
			return nil
		}
	}
	m.UsageEvents = append(m.UsageEvents, event)
	return nil
}

func (m *Fake) UpdateUsageEventContext(_ context.Context, eventID string, used, total int64) error {
	if eventID == "" || used <= 0 || total <= 0 {
		return nil
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i := range m.UsageEvents {
		if m.UsageEvents[i].EventID == eventID {
			m.UsageEvents[i].ContextUsedTokens = used
			m.UsageEvents[i].ContextWindowTokens = total
		}
	}
	return nil
}

func (m *Fake) ConversationCostUSD(_ context.Context, conversationID string) (float64, error) {
	if conversationID == "" {
		return 0, nil
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	var total float64
	for _, e := range m.UsageEvents {
		if e.ConversationID == conversationID {
			total += e.CostUSD
		}
	}
	return total, nil
}

// usageEventRow is one usage event with the joined columns the SQLite query
// reads from agents, users, conversations and cron_jobs.
type usageEventRow struct {
	store.UsageEvent
	ownerID, ownerName, agentName, conversationTitle, cronName, cronDescription string
}

func (r usageEventRow) totalTokens() int64 {
	return r.InputTokens + r.CacheReadInputTokens + r.CacheCreationInputTokens + r.OutputTokens
}

// usageEventRows resolves the joins. Caller holds QueueMu.
func (m *Fake) usageEventRows() []usageEventRow {
	users := make(map[string]store.User, len(m.Users))
	for _, u := range m.Users {
		users[u.ID] = u
	}
	titles := make(map[string]string, len(m.Conversations))
	for _, c := range m.Conversations {
		titles[c.ID] = c.Title
	}
	jobs := make(map[string]store.CronJob, len(m.CronJobs))
	for _, j := range m.CronJobs {
		jobs[j.ID] = j
	}
	rows := make([]usageEventRow, 0, len(m.UsageEvents))
	for _, e := range m.UsageEvents {
		r := usageEventRow{UsageEvent: e, ownerID: e.AgentID}
		if agent, ok := users[e.AgentID]; ok && agent.Username == "" {
			r.agentName = agent.Name
			if agent.OwnerID != "" {
				r.ownerID = agent.OwnerID
			}
		}
		if owner, ok := users[r.ownerID]; ok && owner.Username != "" {
			r.ownerName = owner.Name
		}
		r.conversationTitle = titles[e.ConversationID]
		if j, ok := jobs[e.CronJobID]; ok {
			r.cronDescription = j.Description
			r.cronName = firstNonEmpty(j.Description, j.Prompt, e.CronJobID)
		} else {
			r.cronName = e.CronJobID
		}
		rows = append(rows, r)
	}
	return rows
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func usageEventMatches(r usageEventRow, q store.UsageInsightsQuery) bool {
	if r.Day < q.Start || r.Day > q.End {
		return false
	}
	for _, f := range []struct{ want, got string }{
		{q.ScopeOwnerID, r.ownerID}, {q.OwnerID, r.ownerID}, {q.AgentID, r.AgentID},
		{q.Provider, r.Provider}, {q.Model, r.Model}, {q.ConversationID, r.ConversationID},
		{q.SourceType, r.SourceType},
	} {
		if f.want != "" && f.want != f.got {
			return false
		}
	}
	return true
}

func filterUsageRows(rows []usageEventRow, q store.UsageInsightsQuery) []usageEventRow {
	out := make([]usageEventRow, 0, len(rows))
	for _, r := range rows {
		if usageEventMatches(r, q) {
			out = append(out, r)
		}
	}
	return out
}

// QueryUsageInsights evaluates the dashboard over UsageEvents in memory with
// the SQLite query's filters, grouping, ordering and paging. LastActivity is
// rendered as RFC 3339 rather than in the driver's text format.
func (m *Fake) QueryUsageInsights(_ context.Context, q store.UsageInsightsQuery, thresholds store.UsageThresholds) (store.UsageInsightsResponse, error) {
	store.NormalizeUsageInsightsQuery(&q)
	m.QueueMu.Lock()
	all := m.usageEventRows()
	m.QueueMu.Unlock()

	matched := filterUsageRows(all, q)
	current := summarizeUsage(matched)

	start, _ := time.Parse("2006-01-02", q.Start)
	end, _ := time.Parse("2006-01-02", q.End)
	days := int(end.Sub(start).Hours()/24) + 1
	prevQ := q
	prevQ.End = start.AddDate(0, 0, -1).Format("2006-01-02")
	prevQ.Start = start.AddDate(0, 0, -days).Format("2006-01-02")
	current.Comparison = compareUsage(current, summarizeUsage(filterUsageRows(all, prevQ)))

	rankings, totalRows := rankUsage(matched, q, thresholds)
	return store.UsageInsightsResponse{
		Summary: current, Composition: composeUsage(matched, q.GroupBy), Rankings: rankings,
		Facets: usageFacets(all, q), Page: q.Page, PageSize: q.PageSize, TotalRows: totalRows,
		GroupBy: q.GroupBy, RankBy: q.RankBy, Thresholds: thresholds,
	}, nil
}

func summarizeUsage(rows []usageEventRow) store.UsageSummary {
	var out store.UsageSummary
	var cacheRead int64
	convs := map[string]struct{}{}
	for _, r := range rows {
		out.TotalTokens += r.totalTokens()
		out.CostUSD += r.CostUSD
		out.UserInstructions += r.UserInstructionCount
		out.ModelRequests += r.ModelRequestCount
		out.ToolCalls += r.ToolCallCount
		cacheRead += r.CacheReadInputTokens
		if r.ConversationID != "" {
			convs[r.ConversationID] = struct{}{}
		}
		out.HistoricalEstimate = out.HistoricalEstimate || r.HistoricalEstimate
	}
	out.ActiveConversations = int64(len(convs))
	if out.TotalTokens > 0 {
		out.CacheReadRatio = float64(cacheRead) / float64(out.TotalTokens)
	}
	return out
}

func percentChange(current, previous float64) *float64 {
	if previous == 0 {
		return nil
	}
	v := (current - previous) / previous * 100
	return &v
}

func compareUsage(current, previous store.UsageSummary) store.UsageMetricComparison {
	return store.UsageMetricComparison{
		TotalTokens:         percentChange(float64(current.TotalTokens), float64(previous.TotalTokens)),
		CostUSD:             percentChange(current.CostUSD, previous.CostUSD),
		UserInstructions:    percentChange(float64(current.UserInstructions), float64(previous.UserInstructions)),
		ActiveConversations: percentChange(float64(current.ActiveConversations), float64(previous.ActiveConversations)),
		ModelRequests:       percentChange(float64(current.ModelRequests), float64(previous.ModelRequests)),
		ToolCalls:           percentChange(float64(current.ToolCalls), float64(previous.ToolCalls)),
		CacheReadRatio:      percentChange(current.CacheReadRatio, previous.CacheReadRatio),
	}
}

// usageDimension returns the (key, label) pair SQLite groups by for a
// composition group or ranking dimension.
func usageDimension(r usageEventRow, dim string) (string, string) {
	switch dim {
	case "user":
		return r.ownerID, firstNonEmpty(r.ownerName, r.ownerID)
	case "agent":
		return r.AgentID, firstNonEmpty(r.agentName, r.AgentID)
	case "model":
		return r.Model, r.Model
	case "conversation":
		return r.ConversationID, firstNonEmpty(r.conversationTitle, r.ConversationID)
	case "cron":
		return r.CronJobID, r.cronName
	default:
		return r.Day, r.Day
	}
}

func composeUsage(rows []usageEventRow, groupBy string) []store.UsageCompositionPoint {
	type bucket struct {
		point  store.UsageCompositionPoint
		minDay string
	}
	byKey := map[string]*bucket{}
	var order []string
	for _, r := range rows {
		key, label := usageDimension(r, groupBy)
		b, ok := byKey[key]
		if !ok {
			b = &bucket{point: store.UsageCompositionPoint{Key: key, Label: label}, minDay: r.Day}
			byKey[key] = b
			order = append(order, key)
		}
		if r.Day < b.minDay {
			b.minDay = r.Day
		}
		b.point.InputTokens += r.InputTokens
		b.point.CacheReadInputTokens += r.CacheReadInputTokens
		b.point.CacheCreationInputTokens += r.CacheCreationInputTokens
		b.point.OutputTokens += r.OutputTokens
		b.point.ReasoningOutputTokens += r.ReasoningOutputTokens
		b.point.CostUSD += r.CostUSD
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byKey[order[i]], byKey[order[j]]
		if a.minDay != b.minDay {
			return a.minDay < b.minDay
		}
		return a.point.Label < b.point.Label
	})
	out := make([]store.UsageCompositionPoint, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key].point)
	}
	return out
}

func maxString(a, b string) string {
	if b > a {
		return b
	}
	return a
}

type usageRank struct {
	row          store.UsageRankingRow
	label        string
	totalTokens  int64
	lastActivity time.Time
	minScope     string
	maxScope     string
}

func rankUsage(rows []usageEventRow, q store.UsageInsightsQuery, thresholds store.UsageThresholds) ([]store.UsageRankingRow, int64) {
	byKey := map[string]*usageRank{}
	var order []string
	for _, r := range rows {
		if (q.RankBy == "cron" && r.CronJobID == "") || (q.RankBy == "conversation" && r.ConversationID == "") {
			continue
		}
		key, label := usageDimension(r, q.RankBy)
		g, ok := byKey[key]
		if !ok {
			g = &usageRank{row: store.UsageRankingRow{Key: key}, label: label, minScope: r.RequestCountScope, maxScope: r.RequestCountScope}
			byKey[key] = g
			order = append(order, key)
		}
		row := &g.row
		row.OwnerID = maxString(row.OwnerID, r.ownerID)
		row.OwnerName = maxString(row.OwnerName, r.ownerName)
		row.AgentID = maxString(row.AgentID, r.AgentID)
		row.AgentName = maxString(row.AgentName, r.agentName)
		row.ConversationID = maxString(row.ConversationID, r.ConversationID)
		row.ConversationTitle = maxString(row.ConversationTitle, r.conversationTitle)
		row.CronJobID = maxString(row.CronJobID, r.CronJobID)
		row.CronJobName = maxString(row.CronJobName, r.cronDescription)
		row.Provider = maxString(row.Provider, r.Provider)
		row.Model = maxString(row.Model, r.Model)
		row.SourceType = maxString(row.SourceType, r.SourceType)
		row.UserInstructions += r.UserInstructionCount
		row.ModelRequests += r.ModelRequestCount
		row.ToolCalls += r.ToolCallCount
		row.MaxTaskModelRequests = max(row.MaxTaskModelRequests, r.ModelRequestCount)
		row.MaxTaskToolCalls = max(row.MaxTaskToolCalls, r.ToolCallCount)
		row.InputTokens += r.InputTokens
		row.CacheReadInputTokens += r.CacheReadInputTokens
		row.CacheCreationInputTokens += r.CacheCreationInputTokens
		row.OutputTokens += r.OutputTokens
		row.ReasoningOutputTokens += r.ReasoningOutputTokens
		row.CostUSD += r.CostUSD
		if r.ContextWindowTokens > 0 {
			row.ContextUsageRatio = max(row.ContextUsageRatio, float64(r.ContextUsedTokens)/float64(r.ContextWindowTokens))
		}
		row.HistoricalEstimate = row.HistoricalEstimate || r.HistoricalEstimate
		g.totalTokens += r.totalTokens()
		if r.OccurredAt.After(g.lastActivity) {
			g.lastActivity = r.OccurredAt
		}
		if r.RequestCountScope < g.minScope {
			g.minScope = r.RequestCountScope
		}
		g.maxScope = maxString(g.maxScope, r.RequestCountScope)
	}

	groups := make([]*usageRank, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		if g.totalTokens > 0 {
			g.row.CacheReadRatio = float64(g.row.CacheReadInputTokens) / float64(g.totalTokens)
		}
		g.row.LastActivity = g.lastActivity.Format(time.RFC3339Nano)
		g.row.RequestCountScope = g.minScope
		if g.minScope != g.maxScope {
			g.row.RequestCountScope = "mixed"
		}
		g.row.Anomalies = make([]store.UsageAnomaly, 0)
		if q.RankBy == "conversation" {
			g.row.Anomalies = store.UsageAnomalies(g.row, thresholds)
		} else if g.row.HistoricalEstimate {
			g.row.Anomalies = append(g.row.Anomalies, store.UsageAnomaly{Code: "historical_estimate", Value: 1, Threshold: 0})
		}
		groups = append(groups, g)
	}

	metric := func(g *usageRank) float64 {
		switch q.SortBy {
		case "total_tokens":
			return float64(g.totalTokens)
		case "cache_ratio":
			return g.row.CacheReadRatio
		case "user_instructions":
			return float64(g.row.UserInstructions)
		case "model_requests":
			return float64(g.row.ModelRequests)
		case "tool_calls":
			return float64(g.row.ToolCalls)
		case "last_activity":
			return float64(g.lastActivity.UnixNano())
		default:
			return g.row.CostUSD
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := metric(groups[i]), metric(groups[j])
		if a != b {
			if q.SortOrder == "asc" {
				return a < b
			}
			return a > b
		}
		return groups[i].label < groups[j].label
	})

	total := int64(len(groups))
	out := make([]store.UsageRankingRow, 0, q.PageSize)
	for i := (q.Page - 1) * q.PageSize; i < len(groups) && len(out) < q.PageSize; i++ {
		out = append(out, groups[i].row)
	}
	return out, total
}

// usageFacets lists the filter options within the date range and the caller's
// scope, ignoring the other filters so a picked value never hides its siblings.
func usageFacets(all []usageEventRow, q store.UsageInsightsQuery) store.UsageFacets {
	base := store.UsageInsightsQuery{Start: q.Start, End: q.End, ScopeOwnerID: q.ScopeOwnerID}
	rows := filterUsageRows(all, base)
	out := store.UsageFacets{
		Users: []store.UsageFacet{}, Agents: []store.UsageFacet{}, Providers: []store.UsageFacet{},
		Models: []store.UsageFacet{}, Conversations: []store.UsageFacet{}, CronJobs: []store.UsageFacet{},
	}
	specs := []struct {
		target *[]store.UsageFacet
		dim    string
		key    func(usageEventRow) string
	}{
		{&out.Users, "user", nil},
		{&out.Agents, "agent", func(r usageEventRow) string { return r.AgentID }},
		{&out.Providers, "provider", func(r usageEventRow) string { return r.Provider }},
		{&out.Models, "model", func(r usageEventRow) string { return r.Model }},
		{&out.Conversations, "conversation", func(r usageEventRow) string { return r.ConversationID }},
		{&out.CronJobs, "cron", func(r usageEventRow) string { return r.CronJobID }},
	}
	for _, spec := range specs {
		seen := map[string]struct{}{}
		for _, r := range rows {
			if spec.key != nil && spec.key(r) == "" {
				continue
			}
			id, label := usageDimension(r, spec.dim)
			if spec.dim == "provider" {
				id, label = r.Provider, r.Provider
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			*spec.target = append(*spec.target, store.UsageFacet{ID: id, Label: label})
		}
		sort.SliceStable(*spec.target, func(i, j int) bool { return (*spec.target)[i].Label < (*spec.target)[j].Label })
		if len(*spec.target) > 500 {
			*spec.target = (*spec.target)[:500]
		}
	}
	return out
}
