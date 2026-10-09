package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// TokenUsageRecord is the per-(user, model, day) aggregate of Claude token
// consumption. Counts sum every turn that landed on that day in UTC. The
// `day` column is stored as a YYYY-MM-DD string so SQLite can index and
// range-scan it without timezone arithmetic.
//
// OwnerID is the human-user id that owns this row's UserID — agents
// (User rows with no username) resolve to whichever human shares their
// email. Humans own themselves; orphan rows fall back to UserID. The
// handler populates this field after AggregateTokenUsage returns; it is
// not stored. Surfaces to the UI so the admin chart can stack-by human
// user without exposing agent uuids.
type TokenUsageRecord struct {
	UserID                   string  `json:"user_id"`
	OwnerID                  string  `json:"owner_id"`
	Model                    string  `json:"model"`
	Day                      string  `json:"day"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
	Turns                    int64   `json:"turns"`
}

// TokenUsageDelta is a single increment fed into AddTokenUsage. UserID and
// Model are mandatory; an empty Model is replaced with "unknown" so missed
// system_init events don't drop the row.
//
// Day is the YYYY-MM-DD bucket the row falls into. The handler computes
// it in the operator-configured Usage.Timezone (UTC if unset), so the
// store stays unaware of timezone policy. Empty Day falls back to UTC's
// "today" — preserves current behaviour for tests and any legacy caller
// that hasn't been updated.
type TokenUsageDelta struct {
	UserID                   string
	Model                    string
	Day                      string
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	CostUSD                  float64
}

// ClaudeModelUsage is one model's cumulative usage in a Claude CLI result.
// Claude emits these counters for the whole provider session, not just the
// result frame that carries them.
type ClaudeModelUsage struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ClaudeUsageEvent combines Claude's session-cumulative fields with the
// top-level per-result cache counters. RecordClaudeTokenUsage atomically turns
// it into token_usage deltas while advancing a durable session checkpoint.
type ClaudeUsageEvent struct {
	ConversationID       string
	SessionID            string
	EventID              string
	UserID               string
	FallbackModel        string
	Day                  string
	Provider             string
	SourceType           string
	CronJobID            string
	OccurredAt           time.Time
	UserInstructionCount int64
	ModelRequestCount    int64
	ToolCallCount        int64
	ContextUsedTokens    int64
	ContextWindowTokens  int64
	RequestCountScope    string

	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	TotalCostUSD             float64
	PerModel                 map[string]ClaudeModelUsage
}

// ClaudeUsageRecorder is implemented by stores that can atomically difference
// Claude's session-cumulative usage. It is separate from Store so focused test
// doubles that never persist usage do not need to emulate this transaction.
type ClaudeUsageRecorder interface {
	// RecordClaudeTokenUsage returns the cost it actually billed: the
	// increase over the session's previous snapshot, or 0 for a repeat.
	RecordClaudeTokenUsage(ctx context.Context, event ClaudeUsageEvent) (float64, error)
}

var _ ClaudeUsageRecorder = (*SQLiteStore)(nil)

type claudeUsageCheckpoint struct {
	TotalCostUSD float64                     `json:"total_cost_usd"`
	PerModel     map[string]ClaudeModelUsage `json:"per_model,omitempty"`
	Seen         bool                        `json:"seen,omitempty"`
}

// TokenUsageQuery filters AggregateTokenUsage. All fields are optional. An
// empty UserID and empty UserIDs return rows for every user; admins use
// that variant. UserIDs takes precedence when non-empty and applies a SQL
// IN filter — the handler uses it to expand a single human-user filter
// into the human's id plus all their agent ids (token_usage rows are
// keyed by the agent that ran the turn, so a literal user_id = humanID
// would never match). Date range is inclusive on both ends and uses
// YYYY-MM-DD strings to match the stored format.
type TokenUsageQuery struct {
	UserID   string
	UserIDs  []string
	Model    string
	StartUTC string
	EndUTC   string
}

// AddTokenUsage applies one usage delta to the (user_id, model, day) row.
// The ON CONFLICT clause sums the new values into any existing row so two
// turns finishing on the same UTC day collapse into a single record. Day
// is computed from the server's wall clock in UTC at insert time so a
// later read can range-query without translating timestamps.
func (s *SQLiteStore) AddTokenUsage(ctx context.Context, delta TokenUsageDelta) error {
	return addTokenUsage(ctx, s.db, delta)
}

type tokenUsageExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func addTokenUsage(ctx context.Context, execer tokenUsageExecer, delta TokenUsageDelta) error {
	if delta.UserID == "" {
		return errors.New("token usage: user_id required")
	}
	model := strings.TrimSpace(delta.Model)
	if model == "" {
		model = "unknown"
	}
	day := strings.TrimSpace(delta.Day)
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	_, err := execer.ExecContext(ctx, `
INSERT INTO token_usage (
    user_id, model, day,
    input_tokens, output_tokens,
    cache_read_input_tokens, cache_creation_input_tokens,
    cost_usd, turns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(user_id, model, day) DO UPDATE SET
    input_tokens              = input_tokens + excluded.input_tokens,
    output_tokens             = output_tokens + excluded.output_tokens,
    cache_read_input_tokens   = cache_read_input_tokens + excluded.cache_read_input_tokens,
    cache_creation_input_tokens = cache_creation_input_tokens + excluded.cache_creation_input_tokens,
    cost_usd                  = cost_usd + excluded.cost_usd,
    turns                     = turns + 1`,
		delta.UserID, model, day,
		delta.InputTokens, delta.OutputTokens,
		delta.CacheReadInputTokens, delta.CacheCreationInputTokens,
		delta.CostUSD,
	)
	return err
}

// RecordClaudeTokenUsage records only the increase since the previous Claude
// CLI result for this conversation/session. The checkpoint and token_usage
// writes share one transaction, so concurrent duplicate frames cannot both
// observe the same old baseline and bill it twice.
func (s *SQLiteStore) RecordClaudeTokenUsage(ctx context.Context, event ClaudeUsageEvent) (float64, error) {
	if event.ConversationID == "" {
		return 0, errors.New("claude token usage: conversation_id required")
	}
	if event.UserID == "" {
		return 0, errors.New("claude token usage: user_id required")
	}
	normalizeClaudeUsageEvent(&event)

	var billed float64
	err := s.withTxRetry(ctx, func(tx *sql.Tx) error {
		var err error
		billed, err = recordClaudeTokenUsageTx(ctx, tx, event)
		return err
	})
	return billed, err
}

func recordClaudeTokenUsageTx(ctx context.Context, tx *sql.Tx, event ClaudeUsageEvent) (billed float64, err error) {
	// This INSERT is deliberately the transaction's first statement. It takes
	// the SQLite write lock before we read the old checkpoint, avoiding the
	// stale read-then-write snapshot that busy_timeout cannot repair.
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO claude_usage_checkpoints
    (conversation_id, session_id, snapshot, updated_at)
VALUES (?, ?, '', datetime('now'))`, event.ConversationID, event.SessionID)
	if err != nil {
		return 0, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	eventKey, err := claudeUsageEventKey(event)
	if err != nil {
		return 0, err
	}
	result, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO claude_usage_events
    (conversation_id, session_id, event_key)
VALUES (?, ?, ?)`, event.ConversationID, event.SessionID, eventKey)
	if err != nil {
		return 0, err
	}
	accepted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if accepted == 0 {
		return 0, nil
	}

	previous := claudeUsageCheckpoint{PerModel: make(map[string]ClaudeModelUsage)}
	if inserted == 0 {
		var raw string
		if err := tx.QueryRowContext(ctx, `
SELECT snapshot FROM claude_usage_checkpoints
 WHERE conversation_id = ? AND session_id = ?`, event.ConversationID, event.SessionID).Scan(&raw); err != nil {
			return 0, err
		}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &previous); err != nil {
				return 0, fmt.Errorf("decode claude usage checkpoint: %w", err)
			}
		}
	} else {
		// A missing checkpoint on an otherwise established conversation is the
		// upgrade path. The last assistant metadata is the only durable copy of
		// the pre-upgrade cumulative counters, so use it once as the baseline.
		// Once any checkpoint exists for the conversation, a new session means
		// compact/reset and intentionally starts from zero instead.
		var otherSessions int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM claude_usage_checkpoints
 WHERE conversation_id = ? AND session_id != ?`, event.ConversationID, event.SessionID).Scan(&otherSessions); err != nil {
			return 0, err
		}
		if otherSessions == 0 {
			previous, err = latestPersistedClaudeUsage(ctx, tx, event.ConversationID)
			if err != nil {
				return 0, err
			}
		}
	}
	if previous.PerModel == nil {
		previous.PerModel = make(map[string]ClaudeModelUsage)
	}

	deltas, advanced := claudeUsageDeltas(previous, event)
	if len(event.PerModel) > 0 {
		if advanced && (event.CacheReadInputTokens > 0 || event.CacheCreationInputTokens > 0) {
			primary := strings.TrimSpace(event.FallbackModel)
			if primary == "" {
				primary = "unknown"
			}
			delta := deltas[primary]
			delta.UserID = event.UserID
			delta.Model = primary
			delta.Day = event.Day
			delta.CacheReadInputTokens += event.CacheReadInputTokens
			delta.CacheCreationInputTokens += event.CacheCreationInputTokens
			deltas[primary] = delta
		}
	} else if advanced {
		model := strings.TrimSpace(event.FallbackModel)
		if model == "" {
			model = "unknown"
		}
		deltas[model] = TokenUsageDelta{
			UserID:                   event.UserID,
			Model:                    model,
			Day:                      event.Day,
			InputTokens:              event.InputTokens,
			OutputTokens:             event.OutputTokens,
			CacheReadInputTokens:     event.CacheReadInputTokens,
			CacheCreationInputTokens: event.CacheCreationInputTokens,
			CostUSD:                  cumulativeFloatDelta(event.TotalCostUSD, previous.TotalCostUSD),
		}
	}

	models := make([]string, 0, len(deltas))
	for model := range deltas {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		delta := deltas[model]
		if tokenUsageDeltaEmpty(delta) {
			continue
		}
		if err := addTokenUsage(ctx, tx, delta); err != nil {
			return 0, err
		}
		billed += delta.CostUSD
	}
	// The attributed event rows are written in the same transaction as the
	// cumulative checkpoint. A repeated Claude result therefore records neither
	// a second rollup delta nor a second conversation-level event.
	counterModel := strings.TrimSpace(event.FallbackModel)
	if _, ok := deltas[counterModel]; !ok && len(models) > 0 {
		counterModel = models[0]
	}
	for _, model := range models {
		delta := deltas[model]
		if tokenUsageDeltaEmpty(delta) {
			continue
		}
		rowKey := sha256.Sum256([]byte(eventKey + "\x00" + model))
		usageEvent := UsageEvent{
			ID: fmt.Sprintf("claude-%x", rowKey[:16]), EventID: eventKey,
			ConversationID: event.ConversationID, AgentID: event.UserID,
			Provider: event.Provider, Model: model, SourceType: event.SourceType,
			CronJobID: event.CronJobID, OccurredAt: event.OccurredAt, Day: event.Day,
			InputTokens: delta.InputTokens, CacheReadInputTokens: delta.CacheReadInputTokens,
			CacheCreationInputTokens: delta.CacheCreationInputTokens, OutputTokens: delta.OutputTokens,
			CostUSD: delta.CostUSD, ContextUsedTokens: event.ContextUsedTokens,
			ContextWindowTokens: event.ContextWindowTokens, RequestCountScope: event.RequestCountScope,
		}
		if model == counterModel {
			usageEvent.UserInstructionCount = event.UserInstructionCount
			usageEvent.ModelRequestCount = event.ModelRequestCount
			usageEvent.ToolCallCount = event.ToolCallCount
		}
		if err := insertUsageEvent(ctx, tx, usageEvent); err != nil {
			return 0, err
		}
	}

	next := previous
	next.TotalCostUSD = event.TotalCostUSD
	next.Seen = true
	for model, usage := range event.PerModel {
		next.PerModel[model] = usage
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE claude_usage_checkpoints
   SET snapshot = ?, updated_at = datetime('now')
 WHERE conversation_id = ? AND session_id = ?`, string(raw), event.ConversationID, event.SessionID); err != nil {
		return 0, err
	}
	return billed, nil
}

func normalizeClaudeUsageEvent(event *ClaudeUsageEvent) {
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.FallbackModel = strings.TrimSpace(event.FallbackModel)
	event.Day = strings.TrimSpace(event.Day)
	if event.Day == "" {
		event.Day = time.Now().UTC().Format("2006-01-02")
	}
	event.InputTokens = max(event.InputTokens, 0)
	event.OutputTokens = max(event.OutputTokens, 0)
	event.CacheReadInputTokens = max(event.CacheReadInputTokens, 0)
	event.CacheCreationInputTokens = max(event.CacheCreationInputTokens, 0)
	event.UserInstructionCount = max(event.UserInstructionCount, 0)
	event.ModelRequestCount = max(event.ModelRequestCount, 0)
	event.ToolCallCount = max(event.ToolCallCount, 0)
	event.ContextUsedTokens = max(event.ContextUsedTokens, 0)
	event.ContextWindowTokens = max(event.ContextWindowTokens, 0)
	if event.Provider == "" {
		event.Provider = "claude"
	}
	if event.SourceType == "" {
		event.SourceType = "manual"
	}
	if event.RequestCountScope == "" {
		event.RequestCountScope = "provider_reported"
	}
	if event.TotalCostUSD < 0 || math.IsNaN(event.TotalCostUSD) || math.IsInf(event.TotalCostUSD, 0) {
		event.TotalCostUSD = 0
	}
	clean := make(map[string]ClaudeModelUsage, len(event.PerModel))
	for model, usage := range event.PerModel {
		model = strings.TrimSpace(model)
		if model == "" {
			model = "unknown"
		}
		usage.InputTokens = max(usage.InputTokens, 0)
		usage.OutputTokens = max(usage.OutputTokens, 0)
		if usage.CostUSD < 0 || math.IsNaN(usage.CostUSD) || math.IsInf(usage.CostUSD, 0) {
			usage.CostUSD = 0
		}
		clean[model] = usage
	}
	event.PerModel = clean
}

func claudeUsageDeltas(previous claudeUsageCheckpoint, event ClaudeUsageEvent) (map[string]TokenUsageDelta, bool) {
	deltas := make(map[string]TokenUsageDelta, len(event.PerModel))
	advanced := !previous.Seen
	for model, current := range event.PerModel {
		prior, found := previous.PerModel[model]
		reset := found && (current.InputTokens < prior.InputTokens ||
			current.OutputTokens < prior.OutputTokens || current.CostUSD < prior.CostUSD)
		delta := TokenUsageDelta{UserID: event.UserID, Model: model, Day: event.Day}
		switch {
		case !found || reset:
			advanced = true
			delta.InputTokens = current.InputTokens
			delta.OutputTokens = current.OutputTokens
			delta.CostUSD = current.CostUSD
		case found:
			delta.InputTokens = current.InputTokens - prior.InputTokens
			delta.OutputTokens = current.OutputTokens - prior.OutputTokens
			delta.CostUSD = cumulativeFloatDelta(current.CostUSD, prior.CostUSD)
		}
		if !tokenUsageDeltaEmpty(delta) {
			advanced = true
			deltas[model] = delta
		}
	}
	if len(event.PerModel) == 0 {
		advanced = !previous.Seen || event.TotalCostUSD < previous.TotalCostUSD ||
			event.TotalCostUSD > previous.TotalCostUSD
	}
	return deltas, advanced
}

func cumulativeFloatDelta(current, previous float64) float64 {
	if current < previous {
		return current
	}
	return current - previous
}

func tokenUsageDeltaEmpty(delta TokenUsageDelta) bool {
	return delta.InputTokens == 0 && delta.OutputTokens == 0 &&
		delta.CacheReadInputTokens == 0 && delta.CacheCreationInputTokens == 0 &&
		delta.CostUSD == 0
}

func claudeUsageEventKey(event ClaudeUsageEvent) (string, error) {
	key := struct {
		EventID                  string                      `json:"event_id,omitempty"`
		InputTokens              int64                       `json:"input_tokens"`
		OutputTokens             int64                       `json:"output_tokens"`
		CacheReadInputTokens     int64                       `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64                       `json:"cache_creation_input_tokens"`
		TotalCostUSD             float64                     `json:"total_cost_usd"`
		PerModel                 map[string]ClaudeModelUsage `json:"per_model,omitempty"`
	}{
		EventID: event.EventID, InputTokens: event.InputTokens, OutputTokens: event.OutputTokens,
		CacheReadInputTokens: event.CacheReadInputTokens, CacheCreationInputTokens: event.CacheCreationInputTokens,
		TotalCostUSD: event.TotalCostUSD, PerModel: event.PerModel,
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest), nil
}

func latestPersistedClaudeUsage(ctx context.Context, tx *sql.Tx, conversationID string) (claudeUsageCheckpoint, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT metadata FROM messages
 WHERE conversation_id = ? AND role = 'assistant' AND metadata != ''
 ORDER BY rowid DESC`, conversationID)
	if err != nil {
		return claudeUsageCheckpoint{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return claudeUsageCheckpoint{}, err
		}
		var metadata struct {
			Usage claudeUsageCheckpoint `json:"usage"`
		}
		if json.Unmarshal([]byte(raw), &metadata) == nil &&
			(len(metadata.Usage.PerModel) > 0 || metadata.Usage.TotalCostUSD > 0) {
			metadata.Usage.Seen = true
			return metadata.Usage, nil
		}
	}
	if err := rows.Err(); err != nil {
		return claudeUsageCheckpoint{}, err
	}
	return claudeUsageCheckpoint{PerModel: make(map[string]ClaudeModelUsage)}, nil
}

// AggregateTokenUsage returns daily-grouped usage rows ordered ascending by
// (day, user_id, model). Filters on UserID/Model when non-empty;
// StartUTC/EndUTC bound the day range when set. The result is suitable for
// driving a stacked chart (one row per (model, day) pair) without further
// post-processing on the caller side.
func (s *SQLiteStore) AggregateTokenUsage(ctx context.Context, q TokenUsageQuery) ([]TokenUsageRecord, error) {
	var (
		conds []string
		args  []any
	)
	if len(q.UserIDs) > 0 {
		placeholders := make([]string, len(q.UserIDs))
		for i, id := range q.UserIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		conds = append(conds, "user_id IN ("+strings.Join(placeholders, ", ")+")")
	} else if q.UserID != "" {
		conds = append(conds, "user_id = ?")
		args = append(args, q.UserID)
	}
	if q.Model != "" {
		conds = append(conds, "model = ?")
		args = append(args, q.Model)
	}
	if q.StartUTC != "" {
		conds = append(conds, "day >= ?")
		args = append(args, q.StartUTC)
	}
	if q.EndUTC != "" {
		conds = append(conds, "day <= ?")
		args = append(args, q.EndUTC)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	query := `SELECT user_id, model, day,
                  input_tokens, output_tokens,
                  cache_read_input_tokens, cache_creation_input_tokens,
                  cost_usd, turns
             FROM token_usage` + where + `
            ORDER BY day ASC, user_id ASC, model ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []TokenUsageRecord
	for rows.Next() {
		var r TokenUsageRecord
		if err := rows.Scan(
			&r.UserID, &r.Model, &r.Day,
			&r.InputTokens, &r.OutputTokens,
			&r.CacheReadInputTokens, &r.CacheCreationInputTokens,
			&r.CostUSD, &r.Turns,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTokenUsageModels returns the distinct model names that have ever been
// recorded. Used to populate the model filter dropdown in the admin UI so
// it only shows models with at least one row of data. Sorted ascending so
// the UI ordering is stable.
func (s *SQLiteStore) ListTokenUsageModels(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT DISTINCT model FROM token_usage ORDER BY model ASC")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var models []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}
