package projects

import (
	"strings"
	"testing"
	"time"
)

func TestCreateInputValidation(t *testing.T) {
	valid := CreateInput{OrganizationID: "x", Name: "my-api", RepositoryURL: "https://github.com/acme/my-api"}
	if errs := valid.validate(); len(errs) != 0 {
		t.Fatalf("valid input rejected: %v", errs)
	}
	if valid.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", valid.DefaultBranch)
	}

	tests := map[string]struct {
		in    CreateInput
		field string
	}{
		"uppercase name":       {CreateInput{Name: "MyAPI"}, "name"},
		"underscore name":      {CreateInput{Name: "my_api"}, "name"},
		"http url":             {CreateInput{RepositoryURL: "http://github.com/a/b"}, "repository_url"},
		"credentials in url":   {CreateInput{RepositoryURL: "https://user:ghp_token@github.com/a/b"}, "repository_url"},
		"query in url":         {CreateInput{RepositoryURL: "https://github.com/a/b?x=1"}, "repository_url"},
		"ssh url":              {CreateInput{RepositoryURL: "git@github.com:a/b.git"}, "repository_url"},
		"branch with ..":       {CreateInput{DefaultBranch: "feat/../main"}, "default_branch"},
		"branch leading dash":  {CreateInput{DefaultBranch: "-rf"}, "default_branch"},
		"branch .lock":         {CreateInput{DefaultBranch: "main.lock"}, "default_branch"},
		"branch with space":    {CreateInput{DefaultBranch: "my branch"}, "default_branch"},
		"missing organization": {CreateInput{}, "organization_id"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, ok := tt.in.validate()[tt.field]; !ok {
				t.Errorf("expected error on %s", tt.field)
			}
		})
	}
}

func TestUpdateInputValidation(t *testing.T) {
	if errs := (&UpdateInput{}).validate(); errs["body"] == "" {
		t.Error("empty update accepted")
	}
	branch := "release/v1.2"
	if errs := (&UpdateInput{DefaultBranch: &branch}).validate(); len(errs) != 0 {
		t.Errorf("valid branch rejected: %v", errs)
	}
	name := strings.Repeat("a", 64)
	if errs := (&UpdateInput{Name: &name}).validate(); errs["name"] == "" {
		t.Error("64-char name accepted")
	}
}

func TestETagRoundTrip(t *testing.T) {
	p := Project{UpdatedAt: time.Date(2026, 10, 9, 12, 0, 0, 123456000, time.UTC)}
	for _, header := range []string{p.ETag(), "W/" + p.ETag()} {
		got, ok := ParseETag(header)
		if !ok || !got.Equal(p.UpdatedAt) {
			t.Errorf("ParseETag(%s) = %v, %v", header, got, ok)
		}
	}
	if _, ok := ParseETag(`"not-a-number"`); ok {
		t.Error("garbage ETag accepted")
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Errorf("escapeLike = %q", got)
	}
}
