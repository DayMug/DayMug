package service

import (
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// newTestOIDC builds an OIDCService that bypasses discovery. Tests never hit
// HandleCallback (which needs a real provider); they exercise the pure
// access-decision logic via evaluateAccess directly.
func newTestOIDC(cfg config.OIDCConfig) *OIDCService {
	if cfg.GroupClaim == "" {
		cfg.GroupClaim = "groups"
	}
	if cfg.RoleClaim == "" {
		cfg.RoleClaim = "roles"
	}
	if cfg.PermissionClaim == "" {
		cfg.PermissionClaim = "permissions"
	}
	return &OIDCService{cfg: &cfg}
}

func TestEvaluateAccessNoRequirementsAllowsAll(t *testing.T) {
	svc := newTestOIDC(config.OIDCConfig{})
	if reason, ok := svc.evaluateAccess(nil, nil); !ok {
		t.Errorf("expected allow, got deny: %s", reason)
	}
}

func TestEvaluateAccessMatchesPermissionFromUserInfo(t *testing.T) {
	svc := newTestOIDC(config.OIDCConfig{
		RequiredPermissions: []string{"daymug"},
	})
	ui := map[string]any{"permissions": []any{"other", "daymug"}}
	if reason, ok := svc.evaluateAccess(nil, ui); !ok {
		t.Errorf("expected allow, got deny: %s", reason)
	}
}

func TestEvaluateAccessMatchesGroupFromIDToken(t *testing.T) {
	svc := newTestOIDC(config.OIDCConfig{
		RequiredGroups: []string{"acme/IT"},
	})
	id := map[string]any{"groups": []any{"acme/IT"}}
	if _, ok := svc.evaluateAccess(id, nil); !ok {
		t.Errorf("expected allow")
	}
}

func TestEvaluateAccessOrAcrossDimensions(t *testing.T) {
	// Configured: groups required AND roles required. User matches roles
	// but not groups: still allowed (OR semantics).
	svc := newTestOIDC(config.OIDCConfig{
		RequiredGroups: []string{"engineering"},
		RequiredRoles:  []string{"admin"},
	})
	ui := map[string]any{"roles": []any{"admin"}, "groups": []any{"sales"}}
	if reason, ok := svc.evaluateAccess(nil, ui); !ok {
		t.Errorf("expected allow (role match), got deny: %s", reason)
	}
}

func TestEvaluateAccessDeniesWithReason(t *testing.T) {
	svc := newTestOIDC(config.OIDCConfig{
		RequiredPermissions: []string{"daymug"},
	})
	ui := map[string]any{"permissions": []any{"unrelated"}}
	reason, ok := svc.evaluateAccess(nil, ui)
	if ok {
		t.Fatalf("expected deny")
	}
	if !strings.Contains(reason, "permissions") || !strings.Contains(reason, "daymug") {
		t.Errorf("reason should describe miss: %q", reason)
	}
}

func TestClaimToStringsHandlesShapes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"single string", "admin", []string{"admin"}},
		{"empty string", "", nil},
		{"string slice", []any{"a", "b"}, []string{"a", "b"}},
		{"object slice with name", []any{map[string]any{"name": "g1"}, map[string]any{"id": "g2"}}, []string{"g1", "g2"}},
		{"missing", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{}
			if tc.in != nil {
				claims["x"] = tc.in
			}
			got := claimToStrings(claims, "x")
			if len(got) != len(tc.want) {
				t.Fatalf("len got=%v want=%v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got=%v want=%v", got, tc.want)
				}
			}
		})
	}
}

func TestMergeClaimDeduplicates(t *testing.T) {
	id := map[string]any{"groups": []any{"a", "b"}}
	ui := map[string]any{"groups": []any{"b", "c"}}
	got := mergeClaim("groups", id, ui)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestNewOIDCServiceDisabledIsNoop(t *testing.T) {
	svc, err := NewOIDCService(t.Context(), &config.OIDCConfig{Enabled: false})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if svc != nil {
		t.Errorf("expected nil service when disabled, got %#v", svc)
	}
}

func TestNewOIDCServiceNilCfg(t *testing.T) {
	svc, err := NewOIDCService(t.Context(), nil)
	if err != nil || svc != nil {
		t.Errorf("nil cfg should be nil, nil; got %v %v", svc, err)
	}
}
