package enforce

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const duoHTML = `<!doctype html><html><head>
<link rel="preconnect" href="https://d35aaqx5ub95lt.cloudfront.net">
<link rel="stylesheet" href="//fonts.googleapis.com/css?family=x">
<script src="https://d35aaqx5ub95lt.cloudfront.net/js/app.js"></script>
<script src="/static/main.js"></script>
<script src="https://www.googletagmanager.com/gtm.js"></script>
</head><body>
<a href="https://www.facebook.com/duolingo">Facebook</a>
<a href="https://blog.example.org/">blog</a>
<img src="https://simg-ssl.duolingo.com/logo.png">
<iframe src="https://www.youtube.com/embed/abc"></iframe>
<img src="https://i.ytimg.com/vi/abc/0.jpg">
</body></html>`

const appJS = `const api="https://ios-api-2.duolingo.com/2017-06-30";
const img='//d2pur3iezf4d1j.cloudfront.net/images/';
const docs="https://github.com/duolingo/whatever"; const ws="wss://realtime.duolingo.com";
const yt="https://www.youtube.com/watch?v=x"; const x="https://x.com/duolingo";`

func fakeFetch(pages map[string]string, got *[]string) func(*url.URL) (string, error) {
	return func(u *url.URL) (string, error) {
		*got = append(*got, u.String())
		if body, ok := pages[u.String()]; ok {
			return body, nil
		}
		return "", fmt.Errorf("404 %s", u)
	}
}

func TestDiscover(t *testing.T) {
	var fetched []string
	fetch := fakeFetch(map[string]string{
		"https://duolingo.com/":                           duoHTML,
		"https://d35aaqx5ub95lt.cloudfront.net/js/app.js": appJS,
		"https://duolingo.com/static/main.js":             `fetch("https://excess.duolingo.com/x")`,
		"https://www.googletagmanager.com/gtm.js":         `"https://ads.evil.com/"`,
	}, &fetched)
	got, err := Discover("duolingo.com", []string{"blocked.duolingo.com"}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"d2pur3iezf4d1j.cloudfront.net", "d35aaqx5ub95lt.cloudfront.net", "excess.duolingo.com",
		"fonts.googleapis.com", "ios-api-2.duolingo.com", "realtime.duolingo.com",
		"simg-ssl.duolingo.com", "www.googletagmanager.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	// O script de um terceiro aceite também é lido, mas só se aproveitam
	// domínios do próprio site ou de CDNs.
	for _, u := range fetched {
		if u == "https://ads.evil.com/" {
			t.Fatal("seguiu um domínio citado por um terceiro")
		}
	}
}

func TestDiscoverNeverOpensDistractions(t *testing.T) {
	html := `<script src="https://www.youtube.com/iframe_api"></script><img src="https://pbs.twimg.com/a.jpg"><link href="https://static.cdninstagram.com/a.css">`
	var fetched []string
	got, _ := Discover("site.com", nil, fakeFetch(map[string]string{"https://site.com/": html}, &fetched))
	if len(got) != 0 {
		t.Fatalf("abriu %v", got)
	}
}

func TestDiscoverBlockedSitesStayBlocked(t *testing.T) {
	html := `<script src="https://cdn.reddit-like.com/a.js"></script><img src="https://img.blockedsite.com/a.png">`
	var fetched []string
	got, _ := Discover("site.com", []string{"blockedsite.com"}, fakeFetch(map[string]string{"https://site.com/": html}, &fetched))
	if !reflect.DeepEqual(got, []string{"cdn.reddit-like.com"}) {
		t.Fatalf("got %v", got)
	}
}

func TestEnforcerUsesDiscoveredDomains(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	_ = os.WriteFile(hosts, nil, 0o644)
	var fetched []string
	e := &Enforcer{HostsFile: hosts, ResolverDir: filepath.Join(dir, "resolver"), DNS: &DNSProxy{}, Discover: true,
		Fetch: fakeFetch(map[string]string{"https://duolingo.com/": duoHTML}, &fetched)}
	e.Apply([]string{"*"}, []string{"duolingo.com"})
	deadline := time.Now().Add(2 * time.Second)
	for !e.DNS.matches("fonts.googleapis.com") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		e.Apply([]string{"*"}, []string{"duolingo.com"})
	}
	if !e.DNS.matches("fonts.googleapis.com") || e.DNS.matches("www.youtube.com") || e.DNS.matches("googleapis.com") {
		t.Fatal("descobertos não foram aplicados como esperado")
	}
	if _, err := os.Stat(filepath.Join(dir, "resolver", "d35aaqx5ub95lt.cloudfront.net")); err != nil {
		t.Fatal("faltou o resolver do CDN descoberto")
	}
}
