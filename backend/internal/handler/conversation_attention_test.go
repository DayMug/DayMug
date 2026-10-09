package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func attentionFixture() *storetest.Fake {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice", Email: "alice@x"},
		{ID: "alice-agent", Email: "alice@x", OwnerID: "alice"},
		{ID: "bob", Username: "bob", Email: "bob@x"},
		{ID: "bob-agent", Email: "bob@x", OwnerID: "bob"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "mine", UserID: "alice-agent", Title: "Q3 review"},
		{ID: "running", UserID: "alice-agent", Title: "Still going"},
		{ID: "theirs", UserID: "bob-agent", Title: "Bob's"},
	}
	ms.Attention = map[string]string{"mine": store.AttentionDone, "theirs": store.AttentionError}
	return ms
}

func attentionRouter(h *ConversationHandler, authedUID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	r.GET("/api/conversation-attention", h.ListAttention)
	r.POST("/api/conversations/:id/read", h.MarkRead)
	return r
}

func TestListAttentionReturnsOnlyTheCallersConversations(t *testing.T) {
	ms := attentionFixture()
	h := NewConversationHandler(ms)
	h.Drainer = service.NewDrainer()
	defer h.Drainer.JobStartWithInfo(service.DrainerJob{ConversationID: "running"})()
	defer h.Drainer.JobStartWithInfo(service.DrainerJob{ConversationID: "theirs"})()

	rec := httptest.NewRecorder()
	attentionRouter(h, "alice").ServeHTTP(rec, httptest.NewRequest("GET", "/api/conversation-attention", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body attentionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ConversationID != "mine" || body.Items[0].State != store.AttentionDone {
		t.Fatalf("items = %+v, want only alice's finished conversation", body.Items)
	}
	// Live work is named for its owner only; the global activity snapshot
	// carries no titles, so this must not leak bob's.
	if len(body.Titles) != 1 || body.Titles["running"] != "Still going" {
		t.Fatalf("titles = %v, want only alice's running conversation", body.Titles)
	}
}

func TestMarkReadClearsTheFlagAndTellsTheOwnersOtherTabs(t *testing.T) {
	ms := attentionFixture()
	h := NewConversationHandler(ms)
	h.UserHub = service.NewUserHub()
	frames := make(chan []byte, 1)
	h.UserHub.Join("alice", "tab-2", frames)

	rec := httptest.NewRecorder()
	attentionRouter(h, "alice").ServeHTTP(rec, httptest.NewRequest("POST", "/api/conversations/mine/read", http.NoBody))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if ms.Attention["mine"] != "" {
		t.Fatalf("attention = %q, want cleared", ms.Attention["mine"])
	}
	select {
	case frame := <-frames:
		var msg service.ServerMessage
		if err := json.Unmarshal(frame, &msg); err != nil || msg.Type != "attention_changed" || msg.ConversationID != "mine" {
			t.Fatalf("frame = %s", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("no attention_changed frame")
	}
}

func TestMarkReadRejectsSomeoneElsesConversation(t *testing.T) {
	ms := attentionFixture()
	rec := httptest.NewRecorder()
	attentionRouter(NewConversationHandler(ms), "alice").ServeHTTP(rec, httptest.NewRequest("POST", "/api/conversations/theirs/read", http.NoBody))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if ms.Attention["theirs"] != store.AttentionError {
		t.Fatal("forbidden read must not clear the flag")
	}
}
