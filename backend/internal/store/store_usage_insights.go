package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// UsageEvent is one attributable billing row. A provider result that contains
// several models produces one row per model with a shared EventID; task-level
// counters live on exactly one of those rows so grouping never multiplies
// instructions or tool calls.
type UsageEvent struct {
	ID                       string
	EventID                  string
	ConversationID           string
	AgentID                  string
	Provider                 string
	Model                    string
	SourceType               string
	CronJobID                string
	OccurredAt               time.Time
	Day                      string
	UserInstructionCount     int64
	ModelRequestCount        int64
	ToolCallCount            int64
	InputTokens              int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	OutputTokens             int64
	ReasoningOutputTokens    int64
	CostUSD                  float64
	ContextUsedTokens        int64
	ContextWindowTokens      int64
	RequestCountScope        string
	HistoricalEstimate       bool
}

// UsageEventRecorder is deliberately narrower than Store. Stream persistence
// can feature-detect it, while small test doubles unrelated to analytics do
// not have to grow dozens of bookkeeping methods.
type UsageEventRecorder interface {
	RecordUsageEvent(context.Context, UsageEvent) error
	UpdateUsageEventContext(context.Context, string, int64, int64) error
	// ConversationCostUSD sums what a conversation has billed so far. Each
	// row is already an increase (a cumulative Claude report is differenced
	// before it lands here) and Claude's figures include the spend of the
	// sub-agents it spawned, so the sum is the conversation's whole cost.
	ConversationCostUSD(ctx context.Context, conversationID string) (float64, error)
}

func (s *SQLiteStore) RecordUsageEvent(ctx context.Context, event UsageEvent) error {
	return insertUsageEvent(ctx, s.db, event)
}

type usageEventExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertUsageEvent(ctx context.Context, execer usageEventExecer, event UsageEvent) error {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.EventID) == "" {
		return fmt.Errorf("usage event: id and event_id required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.Day == "" {
		event.Day = event.OccurredAt.UTC().Format("2006-01-02")
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
	_, err := execer.ExecContext(ctx, `
INSERT OR IGNORE INTO usage_events (
    id, event_id, conversation_id, agent_id, provider, model,
    source_type, cron_job_id, occurred_at, day,
    user_instruction_count, model_request_count, tool_call_count,
    input_tokens, cache_read_input_tokens, cache_creation_input_tokens,
    output_tokens, reasoning_output_tokens, cost_usd,
    context_used_tokens, context_window_tokens,
    request_count_scope, historical_estimate
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.EventID, event.ConversationID, event.AgentID, event.Provider, event.Model,
		event.SourceType, event.CronJobID, event.OccurredAt.UTC(), event.Day,
		event.UserInstructionCount, event.ModelRequestCount, event.ToolCallCount,
		event.InputTokens, event.CacheReadInputTokens, event.CacheCreationInputTokens,
		event.OutputTokens, event.ReasoningOutputTokens, event.CostUSD,
		event.ContextUsedTokens, event.ContextWindowTokens,
		event.RequestCountScope, event.HistoricalEstimate,
	)
	return err
}

func (s *SQLiteStore) UpdateUsageEventContext(ctx context.Context, eventID string, used, total int64) error {
	if eventID == "" || used <= 0 || total <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE usage_events
   SET context_used_tokens = ?, context_window_tokens = ?
 WHERE event_id = ?`, used, total, eventID)
	return err
}

func (s *SQLiteStore) ConversationCostUSD(ctx context.Context, conversationID string) (float64, error) {
	if conversationID == "" {
		return 0, nil
	}
	var total float64
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(cost_usd), 0) FROM usage_events WHERE conversation_id = ?`, conversationID).Scan(&total)
	return total, err
}

type UsageInsightsQuery struct {
	Start          string
	End            string
	OwnerID        string
	ScopeOwnerID   string
	AgentID        string
	Provider       string
	Model          string
	ConversationID string
	SourceType     string
	GroupBy        string
	RankBy         string
	SortBy         string
	SortOrder      string
	Page           int
	PageSize       int
}

type UsageMetricComparison struct {
	TotalTokens         *float64 `json:"total_tokens"`
	CostUSD             *float64 `json:"cost_usd"`
	UserInstructions    *float64 `json:"user_instructions"`
	ActiveConversations *float64 `json:"active_conversations"`
	ModelRequests       *float64 `json:"model_requests"`
	ToolCalls           *float64 `json:"tool_calls"`
	CacheReadRatio      *float64 `json:"cache_read_ratio"`
}

type UsageSummary struct {
	TotalTokens         int64                 `json:"total_tokens"`
	CostUSD             float64               `json:"cost_usd"`
	UserInstructions    int64                 `json:"user_instructions"`
	ActiveConversations int64                 `json:"active_conversations"`
	ModelRequests       int64                 `json:"model_requests"`
	ToolCalls           int64                 `json:"tool_calls"`
	CacheReadRatio      float64               `json:"cache_read_ratio"`
	HistoricalEstimate  bool                  `json:"historical_estimate"`
	Comparison          UsageMetricComparison `json:"comparison"`
}

type UsageCompositionPoint struct {
	Key                      string  `json:"key"`
	Label                    string  `json:"label"`
	InputTokens              int64   `json:"input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	ReasoningOutputTokens    int64   `json:"reasoning_output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

type UsageAnomaly struct {
	Code      string  `json:"code"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type UsageRankingRow struct {
	Key                      string         `json:"key"`
	OwnerID                  string         `json:"owner_id"`
	OwnerName                string         `json:"owner_name"`
	AgentID                  string         `json:"agent_id"`
	AgentName                string         `json:"agent_name"`
	ConversationID           string         `json:"conversation_id"`
	ConversationTitle        string         `json:"conversation_title"`
	CronJobID                string         `json:"cron_job_id"`
	CronJobName              string         `json:"cron_job_name"`
	Provider                 string         `json:"provider"`
	Model                    string         `json:"model"`
	SourceType               string         `json:"source_type"`
	UserInstructions         int64          `json:"user_instructions"`
	ModelRequests            int64          `json:"model_requests"`
	ToolCalls                int64          `json:"tool_calls"`
	InputTokens              int64          `json:"input_tokens"`
	CacheReadInputTokens     int64          `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64          `json:"cache_creation_input_tokens"`
	OutputTokens             int64          `json:"output_tokens"`
	ReasoningOutputTokens    int64          `json:"reasoning_output_tokens"`
	CacheReadRatio           float64        `json:"cache_read_ratio"`
	CostUSD                  float64        `json:"cost_usd"`
	ContextUsageRatio        float64        `json:"context_usage_ratio"`
	LastActivity             string         `json:"last_activity"`
	HistoricalEstimate       bool           `json:"historical_estimate"`
	RequestCountScope        string         `json:"request_count_scope"`
	Anomalies                []UsageAnomaly `json:"anomalies"`
	MaxTaskModelRequests     int64          `json:"-"`
	MaxTaskToolCalls         int64          `json:"-"`
}

type UsageFacet struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type UsageFacets struct {
	Users         []UsageFacet `json:"users"`
	Agents        []UsageFacet `json:"agents"`
	Providers     []UsageFacet `json:"providers"`
	Models        []UsageFacet `json:"models"`
	Conversations []UsageFacet `json:"conversations"`
	CronJobs      []UsageFacet `json:"cron_jobs"`
}

type UsageThresholds struct {
	CacheReadRatio      float64 `json:"cache_read_ratio"`
	ModelRequests       int64   `json:"model_requests"`
	ToolCalls           int64   `json:"tool_calls"`
	ContextWarningRatio float64 `json:"context_warning_ratio"`
	ConversationCostUSD float64 `json:"conversation_cost_usd"`
}

type UsageInsightsResponse struct {
	Summary     UsageSummary            `json:"summary"`
	Composition []UsageCompositionPoint `json:"composition"`
	Rankings    []UsageRankingRow       `json:"rankings"`
	Facets      UsageFacets             `json:"facets"`
	Page        int                     `json:"page"`
	PageSize    int                     `json:"page_size"`
	TotalRows   int64                   `json:"total_rows"`
	Timezone    string                  `json:"timezone"`
	GroupBy     string                  `json:"group_by"`
	RankBy      string                  `json:"rank_by"`
	Thresholds  UsageThresholds         `json:"thresholds"`
}

const usageInsightsFrom = `
 FROM usage_events e
 LEFT JOIN conversations c ON c.id = e.conversation_id
 LEFT JOIN agents a ON a.id = e.agent_id
 LEFT JOIN users owner ON owner.id = CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END
 LEFT JOIN cron_jobs j ON j.id = e.cron_job_id`

func usageWhere(q UsageInsightsQuery) (string, []any) {
	parts := []string{"e.day >= ?", "e.day <= ?"}
	args := []any{q.Start, q.End}
	filters := []struct {
		value string
		sql   string
	}{
		{q.ScopeOwnerID, "CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END = ?"},
		{q.OwnerID, "CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END = ?"},
		{q.AgentID, "e.agent_id = ?"},
		{q.Provider, "e.provider = ?"},
		{q.Model, "e.model = ?"},
		{q.ConversationID, "e.conversation_id = ?"},
		{q.SourceType, "e.source_type = ?"},
	}
	for _, filter := range filters {
		if filter.value == "" {
			continue
		}
		parts = append(parts, filter.sql)
		args = append(args, filter.value)
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}

func (s *SQLiteStore) QueryUsageInsights(ctx context.Context, q UsageInsightsQuery, thresholds UsageThresholds) (UsageInsightsResponse, error) {
	NormalizeUsageInsightsQuery(&q)
	where, args := usageWhere(q)
	current, err := s.queryUsageSummary(ctx, where, args)
	if err != nil {
		return UsageInsightsResponse{}, err
	}
	prevQ := q
	start, _ := time.Parse("2006-01-02", q.Start)
	end, _ := time.Parse("2006-01-02", q.End)
	days := int(end.Sub(start).Hours()/24) + 1
	prevQ.End = start.AddDate(0, 0, -1).Format("2006-01-02")
	prevQ.Start = start.AddDate(0, 0, -days).Format("2006-01-02")
	prevWhere, prevArgs := usageWhere(prevQ)
	previous, err := s.queryUsageSummary(ctx, prevWhere, prevArgs)
	if err != nil {
		return UsageInsightsResponse{}, err
	}
	current.Comparison = compareUsageSummary(current, previous)

	composition, err := s.queryUsageComposition(ctx, q, where, args)
	if err != nil {
		return UsageInsightsResponse{}, err
	}
	rankings, totalRows, err := s.queryUsageRankings(ctx, q, thresholds, where, args)
	if err != nil {
		return UsageInsightsResponse{}, err
	}
	facets, err := s.queryUsageFacets(ctx, q)
	if err != nil {
		return UsageInsightsResponse{}, err
	}
	return UsageInsightsResponse{
		Summary: current, Composition: composition, Rankings: rankings, Facets: facets,
		Page: q.Page, PageSize: q.PageSize, TotalRows: totalRows,
		GroupBy: q.GroupBy, RankBy: q.RankBy, Thresholds: thresholds,
	}, nil
}

// NormalizeUsageInsightsQuery replaces unknown grouping, ranking and sort keys
// with their defaults and clamps paging. Exported so the in-memory test double
// applies exactly the same defaults.
func NormalizeUsageInsightsQuery(q *UsageInsightsQuery) {
	groups := map[string]bool{"day": true, "user": true, "agent": true, "model": true, "conversation": true}
	ranks := map[string]bool{"user": true, "agent": true, "model": true, "conversation": true, "cron": true}
	sorts := map[string]bool{"cost": true, "total_tokens": true, "cache_ratio": true, "user_instructions": true, "model_requests": true, "tool_calls": true, "last_activity": true}
	if !groups[q.GroupBy] {
		q.GroupBy = "day"
	}
	if !ranks[q.RankBy] {
		q.RankBy = "conversation"
	}
	if !sorts[q.SortBy] {
		q.SortBy = "cost"
	}
	if q.SortOrder != "asc" {
		q.SortOrder = "desc"
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = 20
	}
	if q.PageSize > 100 {
		q.PageSize = 100
	}
}

func (s *SQLiteStore) queryUsageSummary(ctx context.Context, where string, args []any) (UsageSummary, error) {
	var out UsageSummary
	var historical int64
	var cacheRead int64
	err := s.db.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(e.input_tokens + e.cache_read_input_tokens + e.cache_creation_input_tokens + e.output_tokens), 0),
 COALESCE(SUM(e.cost_usd), 0), COALESCE(SUM(e.user_instruction_count), 0),
 COUNT(DISTINCT NULLIF(e.conversation_id, '')), COALESCE(SUM(e.model_request_count), 0),
 COALESCE(SUM(e.tool_call_count), 0), COALESCE(SUM(e.cache_read_input_tokens), 0),
 COALESCE(MAX(e.historical_estimate), 0)`+usageInsightsFrom+where,
		args...).Scan(&out.TotalTokens, &out.CostUSD, &out.UserInstructions,
		&out.ActiveConversations, &out.ModelRequests, &out.ToolCalls, &cacheRead, &historical)
	if err != nil {
		return out, err
	}
	if out.TotalTokens > 0 {
		out.CacheReadRatio = float64(cacheRead) / float64(out.TotalTokens)
	}
	out.HistoricalEstimate = historical != 0
	return out, nil
}

func percentChange(current, previous float64) *float64 {
	if previous == 0 {
		return nil
	}
	v := (current - previous) / previous * 100
	return &v
}

func compareUsageSummary(current, previous UsageSummary) UsageMetricComparison {
	return UsageMetricComparison{
		TotalTokens:         percentChange(float64(current.TotalTokens), float64(previous.TotalTokens)),
		CostUSD:             percentChange(current.CostUSD, previous.CostUSD),
		UserInstructions:    percentChange(float64(current.UserInstructions), float64(previous.UserInstructions)),
		ActiveConversations: percentChange(float64(current.ActiveConversations), float64(previous.ActiveConversations)),
		ModelRequests:       percentChange(float64(current.ModelRequests), float64(previous.ModelRequests)),
		ToolCalls:           percentChange(float64(current.ToolCalls), float64(previous.ToolCalls)),
		CacheReadRatio:      percentChange(current.CacheReadRatio, previous.CacheReadRatio),
	}
}

func usageGroup(group string) (string, string) {
	switch group {
	case "user":
		return "CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END", "COALESCE(NULLIF(owner.name, ''), CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END)"
	case "agent":
		return "e.agent_id", "COALESCE(NULLIF(a.name, ''), e.agent_id)"
	case "model":
		return "e.model", "e.model"
	case "conversation":
		return "e.conversation_id", "COALESCE(NULLIF(c.title, ''), e.conversation_id)"
	default:
		return "e.day", "e.day"
	}
}

func (s *SQLiteStore) queryUsageComposition(ctx context.Context, q UsageInsightsQuery, where string, args []any) ([]UsageCompositionPoint, error) {
	key, label := usageGroup(q.GroupBy)
	rows, err := s.db.QueryContext(ctx, `SELECT `+key+`, `+label+`,
 COALESCE(SUM(e.input_tokens),0), COALESCE(SUM(e.cache_read_input_tokens),0),
 COALESCE(SUM(e.cache_creation_input_tokens),0), COALESCE(SUM(e.output_tokens),0),
 COALESCE(SUM(e.reasoning_output_tokens),0), COALESCE(SUM(e.cost_usd),0)`+
		usageInsightsFrom+where+` GROUP BY `+key+` ORDER BY MIN(e.day), `+label, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]UsageCompositionPoint, 0)
	for rows.Next() {
		var p UsageCompositionPoint
		if err := rows.Scan(&p.Key, &p.Label, &p.InputTokens, &p.CacheReadInputTokens,
			&p.CacheCreationInputTokens, &p.OutputTokens, &p.ReasoningOutputTokens, &p.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func usageRankGroup(rank string) (string, string) {
	switch rank {
	case "user":
		return "CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END", "COALESCE(NULLIF(owner.name, ''), CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END)"
	case "agent":
		return "e.agent_id", "COALESCE(NULLIF(a.name, ''), e.agent_id)"
	case "model":
		return "e.model", "e.model"
	case "cron":
		return "e.cron_job_id", "COALESCE(NULLIF(j.description, ''), NULLIF(j.prompt, ''), e.cron_job_id)"
	default:
		return "e.conversation_id", "COALESCE(NULLIF(c.title, ''), e.conversation_id)"
	}
}

func (s *SQLiteStore) queryUsageRankings(ctx context.Context, q UsageInsightsQuery, thresholds UsageThresholds, where string, args []any) ([]UsageRankingRow, int64, error) {
	group, label := usageRankGroup(q.RankBy)
	if q.RankBy == "cron" {
		where += " AND e.cron_job_id != ''"
	}
	if q.RankBy == "conversation" {
		where += " AND e.conversation_id != ''"
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1`+usageInsightsFrom+where+` GROUP BY `+group+`)`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sortExpr := map[string]string{
		"cost": "cost_usd", "total_tokens": "total_tokens", "cache_ratio": "cache_ratio",
		"user_instructions": "user_instructions", "model_requests": "model_requests",
		"tool_calls": "tool_calls", "last_activity": "last_activity",
	}[q.SortBy]
	query := `SELECT ` + group + `, ` + label + `,
 COALESCE(MAX(CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END),''),
 COALESCE(MAX(owner.name),''), COALESCE(MAX(e.agent_id),''), COALESCE(MAX(a.name),''),
 COALESCE(MAX(e.conversation_id),''), COALESCE(MAX(c.title),''),
 COALESCE(MAX(e.cron_job_id),''), COALESCE(MAX(j.description),''),
 COALESCE(MAX(e.provider),''), COALESCE(MAX(e.model),''), COALESCE(MAX(e.source_type),''),
 COALESCE(SUM(e.user_instruction_count),0) AS user_instructions,
 COALESCE(SUM(e.model_request_count),0) AS model_requests,
 COALESCE(SUM(e.tool_call_count),0) AS tool_calls,
 COALESCE(MAX(e.model_request_count),0), COALESCE(MAX(e.tool_call_count),0),
 COALESCE(SUM(e.input_tokens),0), COALESCE(SUM(e.cache_read_input_tokens),0),
 COALESCE(SUM(e.cache_creation_input_tokens),0), COALESCE(SUM(e.output_tokens),0),
 COALESCE(SUM(e.reasoning_output_tokens),0),
 CASE WHEN SUM(e.input_tokens + e.cache_read_input_tokens + e.cache_creation_input_tokens + e.output_tokens) > 0
      THEN CAST(SUM(e.cache_read_input_tokens) AS REAL) / SUM(e.input_tokens + e.cache_read_input_tokens + e.cache_creation_input_tokens + e.output_tokens)
      ELSE 0 END AS cache_ratio,
 COALESCE(SUM(e.cost_usd),0) AS cost_usd,
 COALESCE(MAX(CASE WHEN e.context_window_tokens > 0 THEN CAST(e.context_used_tokens AS REAL) / e.context_window_tokens ELSE 0 END),0),
 COALESCE(MAX(e.occurred_at),''), COALESCE(MAX(e.historical_estimate),0),
 CASE WHEN MIN(e.request_count_scope) = MAX(e.request_count_scope) THEN MIN(e.request_count_scope) ELSE 'mixed' END,
 COALESCE(SUM(e.input_tokens + e.cache_read_input_tokens + e.cache_creation_input_tokens + e.output_tokens),0) AS total_tokens` +
		usageInsightsFrom + where + ` GROUP BY ` + group + ` ORDER BY ` + sortExpr + ` ` + q.SortOrder + `, ` + label + ` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := s.db.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]UsageRankingRow, 0)
	for rows.Next() {
		var r UsageRankingRow
		var historical int64
		var totalTokens int64
		if err := rows.Scan(&r.Key, new(string), &r.OwnerID, &r.OwnerName, &r.AgentID, &r.AgentName,
			&r.ConversationID, &r.ConversationTitle, &r.CronJobID, &r.CronJobName,
			&r.Provider, &r.Model, &r.SourceType, &r.UserInstructions, &r.ModelRequests, &r.ToolCalls,
			&r.MaxTaskModelRequests, &r.MaxTaskToolCalls,
			&r.InputTokens, &r.CacheReadInputTokens, &r.CacheCreationInputTokens, &r.OutputTokens,
			&r.ReasoningOutputTokens, &r.CacheReadRatio, &r.CostUSD, &r.ContextUsageRatio,
			&r.LastActivity, &historical, &r.RequestCountScope, &totalTokens); err != nil {
			return nil, 0, err
		}
		r.HistoricalEstimate = historical != 0
		r.Anomalies = make([]UsageAnomaly, 0)
		if q.RankBy == "conversation" {
			r.Anomalies = UsageAnomalies(r, thresholds)
		} else if r.HistoricalEstimate {
			r.Anomalies = append(r.Anomalies, UsageAnomaly{Code: "historical_estimate", Value: 1, Threshold: 0})
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// UsageAnomalies flags the thresholds a conversation ranking row crosses.
func UsageAnomalies(r UsageRankingRow, t UsageThresholds) []UsageAnomaly {
	out := make([]UsageAnomaly, 0)
	if r.CacheReadRatio > t.CacheReadRatio {
		out = append(out, UsageAnomaly{Code: "high_cache_read", Value: r.CacheReadRatio, Threshold: t.CacheReadRatio})
	}
	if r.MaxTaskModelRequests > t.ModelRequests {
		out = append(out, UsageAnomaly{Code: "high_model_requests", Value: float64(r.MaxTaskModelRequests), Threshold: float64(t.ModelRequests)})
	}
	if r.MaxTaskToolCalls > t.ToolCalls {
		out = append(out, UsageAnomaly{Code: "high_tool_calls", Value: float64(r.MaxTaskToolCalls), Threshold: float64(t.ToolCalls)})
	}
	if r.ContextUsageRatio >= t.ContextWarningRatio {
		out = append(out, UsageAnomaly{Code: "context_near_compact", Value: r.ContextUsageRatio, Threshold: t.ContextWarningRatio})
	}
	if r.CostUSD > t.ConversationCostUSD {
		out = append(out, UsageAnomaly{Code: "high_cost", Value: r.CostUSD, Threshold: t.ConversationCostUSD})
	}
	if r.HistoricalEstimate {
		out = append(out, UsageAnomaly{Code: "historical_estimate", Value: 1, Threshold: 0})
	}
	return out
}

func (s *SQLiteStore) queryUsageFacets(ctx context.Context, q UsageInsightsQuery) (UsageFacets, error) {
	base := q
	base.OwnerID = ""
	base.AgentID, base.Provider, base.Model, base.ConversationID, base.SourceType = "", "", "", "", ""
	where, args := usageWhere(base)
	type facetSpec struct {
		target            *[]UsageFacet
		key, label, extra string
	}
	out := UsageFacets{
		Users: []UsageFacet{}, Agents: []UsageFacet{}, Providers: []UsageFacet{},
		Models: []UsageFacet{}, Conversations: []UsageFacet{}, CronJobs: []UsageFacet{},
	}
	specs := []facetSpec{
		{&out.Users, "CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END", "COALESCE(NULLIF(owner.name,''), CASE WHEN a.owner_id != '' THEN a.owner_id ELSE e.agent_id END)", ""},
		{&out.Agents, "e.agent_id", "COALESCE(NULLIF(a.name,''), e.agent_id)", "e.agent_id != ''"},
		{&out.Providers, "e.provider", "e.provider", "e.provider != ''"},
		{&out.Models, "e.model", "e.model", "e.model != ''"},
		{&out.Conversations, "e.conversation_id", "COALESCE(NULLIF(c.title,''), e.conversation_id)", "e.conversation_id != ''"},
		{&out.CronJobs, "e.cron_job_id", "COALESCE(NULLIF(j.description,''), NULLIF(j.prompt,''), e.cron_job_id)", "e.cron_job_id != ''"},
	}
	for _, spec := range specs {
		w := where
		if spec.extra != "" {
			w += " AND " + spec.extra
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+spec.key+`, `+spec.label+usageInsightsFrom+w+` GROUP BY `+spec.key+` ORDER BY `+spec.label+` LIMIT 500`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var f UsageFacet
			if err := rows.Scan(&f.ID, &f.Label); err != nil {
				_ = rows.Close()
				return out, err
			}
			*spec.target = append(*spec.target, f)
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
	}
	return out, nil
}
