package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPath(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	for _, base := range []string{"", "relative"} {
		t.Setenv("XDG_CONFIG_HOME", base)
		got, err := Path()
		if err != nil || got != filepath.Join(home, ".config", "i4d", "config.json") {
			t.Fatalf("%s %v", got, err)
		}
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err := Path()
	if err != nil || got != filepath.Join(xdg, "i4d", "config.json") {
		t.Fatalf("%s %v", got, err)
	}
}

func TestLoadAndSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, exists, err := Load(path)
	if err != nil || exists || cfg.Version != 1 {
		t.Fatalf("%+v %v %v", cfg, exists, err)
	}
	if _, err := cfg.Select("missing"); err == nil {
		t.Fatal("missing selected account accepted")
	}
	valid := `{"version":1,"default_account":"home","accounts":{"home":{"username":"a@icloud.com","calendars":["Home"],"timezone":"UTC"},"work":{"username":"b@icloud.com"}}}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, exists, err = Load(path)
	if err != nil || !exists {
		t.Fatalf("%v %v", exists, err)
	}
	account, err := cfg.Select("")
	if err != nil || account.Username != "a@icloud.com" || account.Calendars[0] != "Home" {
		t.Fatalf("%+v %v", account, err)
	}
	account, err = cfg.Select("work")
	if err != nil || account.Username != "b@icloud.com" {
		t.Fatalf("%+v %v", account, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	cases := []string{
		``, `null`, `[]`, `{`, `{} {}`, `{"version":2,"accounts":{}}`,
		`{"version":1}`, `{"version":1,"accounts":null}`,
		`{"version":1,"accounts":{},"default_account":"missing"}`,
		`{"version":1,"accounts":{"a":{}}}`,
		`{"version":1,"accounts":{"bad name":{"username":"a"}}}`,
		`{"version":1,"accounts":{"a":{"username":"a","calendars":[""]}}}`,
		`{"version":1,"accounts":{"a":{"username":"a","timezone":"INVALID"}}}`,
		`{"version":1,"accounts":{"a":{"username":"a","password":"private-password"}}}`,
		`{"version":1,"profile":"full","accounts":{}}`,
		`{"version":1,"accounts":{}} true`,
		strings.Repeat(" ", maxSize+1),
	}
	for _, contents := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := Load(path)
		if err == nil {
			t.Fatalf("accepted %q", contents[:min(len(contents), 100)])
		}
		if strings.Contains(err.Error(), "private-password") {
			t.Fatal("error leaked config value")
		}
	}
	if _, _, err := Load(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
}
