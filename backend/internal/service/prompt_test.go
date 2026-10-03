package service

import (
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
)

func TestBuildSystemPrompt_Persona(t *testing.T) {
	user := store.User{
		RoleDefinition: "You are a frontend developer.",
	}
	result := BuildSystemPrompt(user)

	if !strings.Contains(result, "## Your Identity") {
		t.Error("expected identity section")
	}
	if !strings.Contains(result, "You are a frontend developer.") {
		t.Error("expected role definition content")
	}
}

func TestBuildSystemPrompt_Empty(t *testing.T) {
	user := store.User{}
	result := BuildSystemPrompt(user)
	if result != "" {
		t.Errorf("expected empty prompt, got %q", result)
	}
}
