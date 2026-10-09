package service

import (
	"github.com/DayMug/DayMug/backend/internal/prompts"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// BuildSystemPrompt assembles a system prompt from the agent's persona (its
// role definition). An empty persona yields an empty prompt.
func BuildSystemPrompt(user store.User) string {
	return prompts.UserSystem(user.RoleDefinition)
}
