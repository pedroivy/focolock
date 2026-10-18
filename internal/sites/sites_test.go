package sites

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"youtube.com":                       "youtube.com",
		"https://www.YouTube.com/watch?v=1": "youtube.com",
		"  x.com  ":                         "x.com",
		"*.reddit.com":                      "reddit.com",
		"news.ycombinator.com:443":          "news.ycombinator.com",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "localhost", "apple.com", "foo", "a b.com", "http://"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) deveria falhar", bad)
		}
	}
}

func TestNormalizeAllDedup(t *testing.T) {
	got, err := NormalizeAll([]string{"www.x.com", "x.com", "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a.com", "x.com"}) {
		t.Fatalf("got %v", got)
	}
}

func TestExpand(t *testing.T) {
	got := Expand([]string{"x.com"})
	if len(got) != len(commonSubdomains) || got[0] != "api.x.com" {
		t.Fatalf("got %v", got)
	}
}
