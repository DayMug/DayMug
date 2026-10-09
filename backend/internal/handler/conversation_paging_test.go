package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func seedConversations(n int) []store.Conversation {
	convs := make([]store.Conversation, 0, n)
	for i := range n {
		convs = append(convs, store.Conversation{ID: fmt.Sprintf("c%03d", i), Title: fmt.Sprintf("Chat %d", i)})
	}
	return convs
}

func listConversationIDs(t *testing.T, r http.Handler, url string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", url, http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d body %s", url, rec.Code, rec.Body.String())
	}
	var convs []store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&convs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make([]string, 0, len(convs))
	for _, c := range convs {
		ids = append(ids, c.ID)
	}
	return ids
}

// An unparameterised request is what every pre-paging client sends, so it has
// to keep returning the whole list rather than silently truncating to a page.
func TestConversation_List_WithoutParamsStaysUnpaged(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = seedConversations(store.ConversationPageDefault + 20)
	r := setupRouter(ms)

	if got := len(listConversationIDs(t, r, "/api/conversations?user_id=u1")); got != store.ConversationPageDefault+20 {
		t.Fatalf("expected the full list, got %d rows", got)
	}
}

func TestConversation_List_LimitAndOffsetWalkTheListOnce(t *testing.T) {
	const total = 25
	ms := storetest.New()
	ms.Conversations = seedConversations(total)
	r := setupRouter(ms)

	seen := make([]string, 0, total)
	for offset := 0; offset < total; offset += 10 {
		page := listConversationIDs(t, r, fmt.Sprintf("/api/conversations?user_id=u1&limit=10&offset=%d", offset))
		seen = append(seen, page...)
	}

	if len(seen) != total {
		t.Fatalf("walking pages yielded %d rows, want %d", len(seen), total)
	}
	for i, id := range seen {
		want := fmt.Sprintf("c%03d", i)
		if id != want {
			t.Fatalf("row %d: got %q, want %q (paging skipped or repeated a row)", i, id, want)
		}
	}
}

func TestConversation_List_OffsetPastEndReturnsEmptyArray(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = seedConversations(3)
	r := setupRouter(ms)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/conversations?user_id=u1&limit=10&offset=99", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// `null` would make the SPA's `.length` blow up; the contract is an array.
	if body := rec.Body.String(); body != "[]" {
		t.Fatalf("expected an empty array, got %s", body)
	}
}

// A garbage limit must not become a 500 or a zero-row page — it falls back to
// the unparameterised read, which is the behaviour the caller had before.
func TestConversation_List_UnparseableParamsFallBackToFullList(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = seedConversations(4)
	r := setupRouter(ms)

	if got := len(listConversationIDs(t, r, "/api/conversations?user_id=u1&limit=abc&offset=-5")); got != 4 {
		t.Fatalf("expected 4 rows, got %d", got)
	}
}

func TestAppState_PreloadsOnePageAndFlagsMore(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", Email: "a@example.com"},
		{ID: "agent-a", Email: "a@example.com", OwnerID: "human-a"},
	}
	ms.Conversations = seedConversations(store.ConversationPageDefault + 1)
	r := setupAppStateRouter(t, NewAppStateHandler(ms, nil, "1.2.3"), "human-a")

	w := doJSON(r, "GET", "/api/app-state?user_id=agent-a", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Conversations []store.Conversation `json:"conversations"`
		HasMore       bool                 `json:"conversations_has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Conversations) != store.ConversationPageDefault {
		t.Fatalf("preloaded %d rows, want one page of %d", len(body.Conversations), store.ConversationPageDefault)
	}
	if !body.HasMore {
		t.Fatal("expected conversations_has_more=true when a row is left over")
	}
}

func TestAppState_ExactlyOnePageDoesNotFlagMore(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", Email: "a@example.com"},
		{ID: "agent-a", Email: "a@example.com", OwnerID: "human-a"},
	}
	ms.Conversations = seedConversations(store.ConversationPageDefault)
	r := setupAppStateRouter(t, NewAppStateHandler(ms, nil, "1.2.3"), "human-a")

	w := doJSON(r, "GET", "/api/app-state?user_id=agent-a", nil)
	var body struct {
		Conversations []store.Conversation `json:"conversations"`
		HasMore       bool                 `json:"conversations_has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Conversations) != store.ConversationPageDefault {
		t.Fatalf("preloaded %d rows", len(body.Conversations))
	}
	// Off-by-one here shows up as a "Load more" button that fetches nothing.
	if body.HasMore {
		t.Fatal("expected conversations_has_more=false when the list fits exactly")
	}
}
