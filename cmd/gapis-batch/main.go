// Command gapis-batch converts the entire set of Google APIs (as served by the
// Google API Discovery Service) into .proto files using openapi2proto.
//
// It fetches the discovery directory, then for each API fetches its discovery
// document, converts it to proto, and writes the result under -out. A JSON
// report summarizing every API's outcome is written to <out>/report.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/accretional/openapi2proto/generator"
)

const directoryURL = "https://discovery.googleapis.com/discovery/v1/apis"

type apiItem struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Version          string `json:"version"`
	Title            string `json:"title"`
	DiscoveryRestURL string `json:"discoveryRestUrl"`
	Preferred        bool   `json:"preferred"`
}

type directory struct {
	Items []apiItem `json:"items"`
}

type result struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	URL        string `json:"discoveryRestUrl"`
	Stage      string `json:"stage"` // fetch | decode | generate | done
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DiscoBytes int    `json:"disco_bytes,omitempty"`
	ProtoBytes int    `json:"proto_bytes,omitempty"`
	ProtoPath  string `json:"proto_path,omitempty"`
	// Entrypoint wiring (set when -go_module is given).
	BaseURL string `json:"base_url,omitempty"`
	Import  string `json:"import,omitempty"`
	Alias   string `json:"alias,omitempty"`
}

func main() {
	out := flag.String("out", "out", "output directory")
	workers := flag.Int("workers", 16, "number of concurrent workers")
	limit := flag.Int("limit", 0, "process at most N apis (0 = all)")
	cacheDisco := flag.Bool("cache_disco", true, "save fetched discovery docs under <out>/discovery")
	goModule := flag.String("go_module", "", "Go module path; when set, also emit gRPC service stubs under <out>/service")
	flag.Parse()

	opts := genOpts{out: *out, cacheDisco: *cacheDisco, goModule: *goModule}

	items, err := loadDirectory()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error loading directory:", err)
		os.Exit(1)
	}
	// Best-effort supplements present in the HTML list but absent from the
	// discovery directory and still serving a discovery document.
	items = append(items,
		apiItem{ID: "photoslibrary:v1", Name: "photoslibrary", Version: "v1", DiscoveryRestURL: "https://photoslibrary.googleapis.com/$discovery/rest?version=v1"},
		apiItem{ID: "sourcerepo:v1", Name: "sourcerepo", Version: "v1", DiscoveryRestURL: "https://sourcerepo.googleapis.com/$discovery/rest?version=v1"},
	)
	if *limit > 0 && *limit < len(items) {
		items = items[:*limit]
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	jobs := make(chan apiItem)
	results := make(chan result)

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				results <- convert(client, it, opts)
			}
		}()
	}
	go func() {
		for _, it := range items {
			jobs <- it
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(results) }()

	var all []result
	var done int
	total := len(items)
	for r := range results {
		all = append(all, r)
		done++
		status := "OK "
		if !r.OK {
			status = "ERR"
		}
		fmt.Printf("[%3d/%3d] %s %-40s %s\n", done, total, status, r.ID, r.Error)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	report, _ := json.MarshalIndent(all, "", "  ")
	os.WriteFile(filepath.Join(*out, "report.json"), report, 0o644)

	// Emit the generic gRPC entrypoint that can serve any one converted API.
	if *goModule != "" {
		var entries []generator.ServerEntry
		for _, r := range all {
			if r.OK && r.Import != "" {
				entries = append(entries, generator.ServerEntry{
					API:     r.Name,
					Version: r.Version,
					BaseURL: r.BaseURL,
					Import:  r.Import,
					Alias:   r.Alias,
				})
			}
		}
		main := generator.GenerateServerMain(*goModule, entries)
		dir := filepath.Join(*out, "cmd", "server")
		os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "main.go"), main, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "error writing entrypoint:", err)
		} else {
			fmt.Printf("wrote entrypoint cmd/server/main.go (%d apis)\n", len(entries))
		}
	}

	summarize(all, *out)
}

func loadDirectory() ([]apiItem, error) {
	// Prefer a locally cached directory if present (avoids re-fetch on rerun).
	if b, err := os.ReadFile("/tmp/gapis_discovery.json"); err == nil {
		var d directory
		if json.Unmarshal(b, &d) == nil && len(d.Items) > 0 {
			return d.Items, nil
		}
	}
	resp, err := http.Get(directoryURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var d directory
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return d.Items, nil
}

// genOpts carries per-run output settings to the workers.
type genOpts struct {
	out        string
	cacheDisco bool
	goModule   string // when non-empty, also emit gRPC service stubs
}

// nonIdent matches any character not valid in a Go/proto path or identifier
// segment. Most Google API names/versions are already clean; a handful carry
// dots or camelCase (e.g. "content"/"v2.1", "youtubeAnalytics"/"v2").
var nonIdent = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// pkgPaths derives a single coherent set of paths/identifiers for an API so the
// proto directory, the protoc output directory, the go_package option, and the
// service's pb import all agree:
//
//	path  = "<name>/<verSan>"      e.g. "youtubeAnalytics/v2", "content/v2_1"
//	alias = "<name><verSan>"       lowercased, identifier-safe Go package name
func pkgPaths(name, version string) (path, alias string) {
	verSan := nonIdent.ReplaceAllString(version, "_")
	nameSan := nonIdent.ReplaceAllString(name, "_")
	path = nameSan + "/" + verSan
	alias = strings.ToLower(nonIdent.ReplaceAllString(name+verSan, ""))
	return path, alias
}

// discoveryBaseURL extracts the REST base URL (no trailing slash) a runtime
// client should target, from a Google Discovery Document. Generated operation
// paths are relative to baseUrl (= rootUrl + servicePath), so that is the
// correct base; rootUrl+servicePath is used as a fallback.
func discoveryBaseURL(data []byte) string {
	var d struct {
		BaseURL     string `json:"baseUrl"`
		RootURL     string `json:"rootUrl"`
		ServicePath string `json:"servicePath"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return ""
	}
	base := d.BaseURL
	if base == "" {
		base = strings.TrimRight(d.RootURL, "/") + "/" + strings.TrimLeft(d.ServicePath, "/")
	}
	return strings.TrimRight(base, "/")
}

func convert(client *http.Client, it apiItem, o genOpts) result {
	r := result{ID: it.ID, Name: it.Name, Version: it.Version, URL: it.DiscoveryRestURL, Stage: "fetch"}

	req, _ := http.NewRequest("GET", it.DiscoveryRestURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if resp.StatusCode != 200 {
		r.Error = fmt.Sprintf("http %d", resp.StatusCode)
		return r
	}
	r.DiscoBytes = len(data)
	if o.cacheDisco {
		dir := filepath.Join(o.out, "discovery")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, it.Name+"."+it.Version+".json"), data, 0o644)
	}

	r.Stage = "decode"
	doc, err := generator.DecodeOpenAPIFromBytes(data)
	if err != nil {
		r.Error = err.Error()
		return r
	}

	r.Stage = "generate"
	path, alias := pkgPaths(it.Name, it.Version)
	pbSubPath := "pb/" + path
	source := it.Name + "." + it.Version
	cfg := generator.Config{
		PackageName:         source,
		GoPackage:           path + ";" + alias,
		ServiceGrouping:     "tag",
		EmitHTTPAnnotations: true,
	}
	proto, err := generator.Generate(source, doc, cfg)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.ProtoBytes = len(proto)

	protoDir := filepath.Join(o.out, "proto", filepath.FromSlash(path))
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		r.Error = err.Error()
		return r
	}
	protoPath := filepath.Join(protoDir, it.Name+".proto")
	if err := os.WriteFile(protoPath, proto, 0o644); err != nil {
		r.Error = err.Error()
		return r
	}
	r.ProtoPath = protoPath

	if o.goModule != "" {
		svc, err := generator.GenerateGoService(source, doc, cfg, o.goModule, pbSubPath, "")
		if err != nil {
			r.Stage = "service"
			r.Error = err.Error()
			return r
		}
		if len(svc) > 0 {
			svcDir := filepath.Join(o.out, "service", filepath.FromSlash(path))
			if err := os.MkdirAll(svcDir, 0o755); err != nil {
				r.Error = err.Error()
				return r
			}
			if err := os.WriteFile(filepath.Join(svcDir, "server.go"), svc, 0o644); err != nil {
				r.Error = err.Error()
				return r
			}
			r.BaseURL = discoveryBaseURL(data)
			r.Import = o.goModule + "/service/" + path
			r.Alias = alias
		}
	}

	r.Stage = "done"
	r.OK = true
	return r
}

func summarize(all []result, out string) {
	var ok int
	byStage := map[string]int{}
	for _, r := range all {
		if r.OK {
			ok++
		} else {
			byStage[r.Stage]++
		}
	}
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("total: %d   ok: %d   failed: %d\n", len(all), ok, len(all)-ok)
	if len(all)-ok > 0 {
		fmt.Println("failures by stage:")
		stages := make([]string, 0, len(byStage))
		for s := range byStage {
			stages = append(stages, s)
		}
		sort.Strings(stages)
		for _, s := range stages {
			fmt.Printf("  %-10s %d\n", s, byStage[s])
		}
	}
	fmt.Printf("report: %s\n", filepath.Join(out, "report.json"))
}
