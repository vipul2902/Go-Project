package projects

import (
	"net/url"
	"testing"
)

func FuzzRepositoryURLValidation(f *testing.F) {
	for _, s := range []string{"https://github.com/a/b", "https://u:p@github.com/a/b", "file:///etc", "https://x?y", "HTTPS://github.com/a/b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		errs := map[string]string{}
		validateRepoURL(raw, errs)
		if errs["repository_url"] != "" {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			t.Fatalf("accepted unsafe repository URL %q", raw)
		}
	})
}

func FuzzParseETag(f *testing.F) {
	f.Add(`"1760000000000000"`)
	f.Add(`W/"1"`)
	f.Fuzz(func(t *testing.T, s string) { _, _ = ParseETag(s) })
}
