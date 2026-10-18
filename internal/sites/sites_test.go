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
		"Internet":                          Internet,
		"*":                                 Internet,
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
	got := Expand([]string{"x.com", Internet})
	if len(got) != len(commonSubdomains) || got[0] != "api.x.com" {
		t.Fatalf("got %v", got)
	}
}

func TestNormalizeAllow(t *testing.T) {
	got, err := NormalizeAllow([]string{Internet}, []string{"https://www.Wikipedia.org/wiki/x", "apple.com", "wikipedia.org", "github.com"})
	if err != nil || len(got) != 2 || got[0] != "github.com" || got[1] != "wikipedia.org" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := NormalizeAllow([]string{Internet}, nil); err != nil || got != nil {
		t.Fatalf("lista vazia: %v, %v", got, err)
	}
	if _, err := NormalizeAllow([]string{"x.com"}, []string{"y.com"}); err == nil {
		t.Error("exceções sem bloqueio da internet deveriam falhar")
	}
	if _, err := NormalizeAllow([]string{Internet}, []string{"internet"}); err == nil {
		t.Error("a internet não pode ser exceção")
	}
}
