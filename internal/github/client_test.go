package github

import (
	"errors"
	"testing"
)

// isolateAuthEnv guarantees TokenForHost resolves nothing ambient:
// no env tokens, an empty gh config dir, and a dead GH_PATH so the
// gh-exec keyring fallback cannot fire.
func isolateAuthEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
}

func TestForHostWithoutCredentialsReturnsTypedError(t *testing.T) {
	isolateAuthEnv(t)
	_, err := ForHost("github.com")
	var noCreds *NoCredentialsError
	if !errors.As(err, &noCreds) || noCreds.Host != "github.com" {
		t.Fatalf("ForHost() error = %v; want NoCredentialsError for github.com", err)
	}
	want := "no GitHub credentials for github.com: set GH_TOKEN or run 'gh auth login'"
	if err.Error() != want {
		t.Fatalf("error text = %q; want %q", err.Error(), want)
	}
}

func TestForHostUsesEnvTokenAndCachesPerHost(t *testing.T) {
	isolateAuthEnv(t)
	t.Setenv("GH_TOKEN", "env-token")
	c1, err := ForHost("github.com")
	if err != nil {
		t.Fatalf("ForHost() error = %v", err)
	}
	c2, err := ForHost("github.com")
	if err != nil {
		t.Fatalf("ForHost() second call error = %v", err)
	}
	if c1 != c2 {
		t.Fatal("ForHost() did not cache the client per host")
	}
}

func TestForHostDefaultsEmptyHostToGitHubCom(t *testing.T) {
	isolateAuthEnv(t)
	t.Setenv("GH_TOKEN", "env-token")
	c1, _ := ForHost("")
	c2, _ := ForHost("github.com")
	if c1 != c2 {
		t.Fatal("empty host should alias github.com")
	}
}

func TestOverrideForTestBypassesCacheAndRestores(t *testing.T) {
	isolateAuthEnv(t)
	restore := OverrideForTest("http://127.0.0.1:1", "test-token")
	c1, err := ForHost("github.com")
	if err != nil {
		t.Fatalf("ForHost() with override error = %v", err)
	}
	c2, _ := ForHost("github.com")
	if c1 == c2 {
		t.Fatal("override clients must not be cached")
	}
	restore()
	if _, err := ForHost("github.com"); err == nil {
		t.Fatal("after restore, credential resolution should apply again")
	}
}
