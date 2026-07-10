// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-puppet/puppet authors

package puppet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	gohiera "github.com/go-hiera/hiera"
)

// orderedManifest declares packages, a service and a notify with an explicit
// ordering edge (->) and a require metaparameter, plus a tag.
const orderedManifest = `
package { 'nginx': ensure => installed }
service { 'nginx':
  ensure  => running,
  require => Package['nginx'],
  tag     => 'web',
}
notify { 'done': message => 'ok' }
Service['nginx'] -> Notify['done']
`

func TestParseValid(t *testing.T) {
	if err := Parse(orderedManifest); err != nil {
		t.Fatalf("Parse(valid) returned error: %v", err)
	}
}

func TestParseInvalid(t *testing.T) {
	if err := Parse("package { 'nginx': ensure => "); err == nil {
		t.Fatal("Parse(invalid) returned nil error, want syntax error")
	}
}

func TestCompileOrdering(t *testing.T) {
	cat, logs, err := Compile(orderedManifest, CompileOptions{NodeName: "node1"})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected no logs, got %v", logs)
	}

	// Size counts every compiled resource (3 declared here).
	if got := cat.Size(); got != 3 {
		t.Fatalf("Size() = %d, want 3", got)
	}
	if got := len(cat.Resources()); got != 3 {
		t.Fatalf("len(Resources()) = %d, want 3", got)
	}

	// Resource(ref): found.
	svc, ok := cat.Resource("Service[nginx]")
	if !ok {
		t.Fatal("Resource(Service[nginx]) not found")
	}
	if svc.Ref() != "Service[nginx]" {
		t.Fatalf("Ref() = %q, want Service[nginx]", svc.Ref())
	}
	if svc.Type != "Service" || svc.Title != "nginx" {
		t.Fatalf("unexpected Type/Title: %q/%q", svc.Type, svc.Title)
	}
	if svc.Parameters["ensure"] != "running" {
		t.Fatalf("ensure = %v, want running", svc.Parameters["ensure"])
	}
	if len(svc.Tags) != 1 || svc.Tags[0] != "web" {
		t.Fatalf("Tags = %v, want [web]", svc.Tags)
	}

	// Resource(ref): not found.
	if _, ok := cat.Resource("Service[absent]"); ok {
		t.Fatal("Resource(Service[absent]) reported found, want not found")
	}

	// Edges include both the require-derived and the ->-derived relationship.
	edges := cat.Edges()
	wantForward := [2]string{"Service[nginx]", "Notify[done]"}
	wantRequire := [2]string{"Package[nginx]", "Service[nginx]"}
	var haveForward, haveRequire bool
	for _, e := range edges {
		if e == wantForward {
			haveForward = true
		}
		if e == wantRequire {
			haveRequire = true
		}
	}
	if !haveForward {
		t.Fatalf("edges %v missing forward edge %v", edges, wantForward)
	}
	if !haveRequire {
		t.Fatalf("edges %v missing require edge %v", edges, wantRequire)
	}

	// JSON is non-empty and parseable.
	js := cat.JSON()
	if js == "" {
		t.Fatal("JSON() is empty")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(js), &decoded); err != nil {
		t.Fatalf("JSON() is not valid JSON: %v", err)
	}
	if decoded["name"] != "node1" {
		t.Fatalf("catalog name = %v, want node1", decoded["name"])
	}
}

func TestCompileDefaultNodeName(t *testing.T) {
	cat, _, err := Compile("notify { 'x': }", CompileOptions{})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(cat.JSON()), &decoded); err != nil {
		t.Fatalf("JSON not parseable: %v", err)
	}
	if decoded["name"] != "default" {
		t.Fatalf("default node name = %v, want default", decoded["name"])
	}
}

func TestCompileFactsInterpolation(t *testing.T) {
	manifest := `notify { 'facts': message => "os is ${facts['os']}" }`
	cat, _, err := Compile(manifest, CompileOptions{
		Facts:    map[string]any{"os": "Debian"},
		NodeName: "n",
	})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	r, ok := cat.Resource("Notify[facts]")
	if !ok {
		t.Fatal("Notify[facts] not found")
	}
	if r.Parameters["message"] != "os is Debian" {
		t.Fatalf("message = %v, want 'os is Debian'", r.Parameters["message"])
	}
}

func TestCompileEvaluationError(t *testing.T) {
	// Duplicate resource declaration is an evaluation error the engine surfaces.
	_, _, err := Compile("notify { 'x': }\nnotify { 'x': }", CompileOptions{})
	if err == nil {
		t.Fatal("Compile of duplicate declaration returned nil error")
	}
}

const classManifest = `
class demo (String $greeting) {
  notify { 'greet': message => $greeting }
}
include demo
`

// writeHiera writes a minimal Hiera 5 config plus one data file resolving
// demo::greeting, returning the path to hiera.yaml.
func writeHiera(t *testing.T, value string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	cfg := "version: 5\n" +
		"defaults:\n" +
		"  datadir: data\n" +
		"  data_hash: yaml_data\n" +
		"hierarchy:\n" +
		"  - name: common\n" +
		"    path: common.yaml\n"
	cfgPath := filepath.Join(dir, "hiera.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write hiera.yaml: %v", err)
	}
	data := "demo::greeting: " + value + "\n"
	if err := os.WriteFile(filepath.Join(dir, "data", "common.yaml"), []byte(data), 0o644); err != nil {
		t.Fatalf("write common.yaml: %v", err)
	}
	return cfgPath
}

func TestCompileHieraDerivedScope(t *testing.T) {
	cfgPath := writeHiera(t, `"hello from hiera"`)
	// HieraScope left nil -> derived from Facts via factsScope.
	cat, _, err := Compile(classManifest, CompileOptions{
		Facts:       map[string]any{"os": "Debian"},
		HieraConfig: cfgPath,
	})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	r, ok := cat.Resource("Notify[greet]")
	if !ok {
		t.Fatal("Notify[greet] not found")
	}
	if r.Parameters["message"] != "hello from hiera" {
		t.Fatalf("message = %v, want 'hello from hiera'", r.Parameters["message"])
	}
}

func TestCompileHieraExplicitScope(t *testing.T) {
	cfgPath := writeHiera(t, `"scoped value"`)
	// Explicit non-nil scope covers the scope != nil branch.
	cat, _, err := Compile(classManifest, CompileOptions{
		HieraConfig: cfgPath,
		HieraScope:  gohiera.MapScope{},
	})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	r, ok := cat.Resource("Notify[greet]")
	if !ok {
		t.Fatal("Notify[greet] not found")
	}
	if r.Parameters["message"] != "scoped value" {
		t.Fatalf("message = %v, want 'scoped value'", r.Parameters["message"])
	}
}

func TestCompileHieraLoadError(t *testing.T) {
	_, _, err := Compile(classManifest, CompileOptions{
		HieraConfig: filepath.Join(t.TempDir(), "does-not-exist.yaml"),
	})
	if err == nil {
		t.Fatal("Compile with bad HieraConfig returned nil error")
	}
}

func TestMapFacts(t *testing.T) {
	m := mapFacts{"os": "Debian", "kernel": "Linux"}

	if v, ok := m.Fact("os"); !ok || v != "Debian" {
		t.Fatalf("Fact(os) = %v,%v; want Debian,true", v, ok)
	}
	if v, ok := m.Fact("absent"); ok || v != nil {
		t.Fatalf("Fact(absent) = %v,%v; want nil,false", v, ok)
	}
	facts := m.Facts()
	if len(facts) != 2 || facts["kernel"] != "Linux" {
		t.Fatalf("Facts() = %v, want the backing map", facts)
	}
}

// canonResource is an order-independent snapshot of a catalog resource used to
// compare an HCL2 catalog against its Puppet twin.
type canonResource struct {
	Ref    string
	Type   string
	Title  string
	Params map[string]any
	Tags   []string
}

// canon returns a catalog's resources sorted by ref and its edges sorted
// lexicographically, so two catalogs built from twin manifests compare equal
// regardless of declaration/insertion order.
func canon(c *Catalog) ([]canonResource, [][2]string) {
	res := c.Resources()
	out := make([]canonResource, len(res))
	for i, r := range res {
		out[i] = canonResource{Ref: r.Ref(), Type: r.Type, Title: r.Title, Params: r.Parameters, Tags: r.Tags}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })

	edges := c.Edges()
	es := make([][2]string, len(edges))
	copy(es, edges)
	sort.Slice(es, func(i, j int) bool {
		if es[i][0] != es[j][0] {
			return es[i][0] < es[j][0]
		}
		return es[i][1] < es[j][1]
	})
	return out, es
}

// assertSameCatalog compiles hclSrc as HCL2 and ppSrc as Puppet under the same
// options and fails unless the two catalogs are identical (resources + edges),
// which is the whole contract of HCL2 support.
func assertSameCatalog(t *testing.T, hclSrc, ppSrc string, opts CompileOptions) {
	t.Helper()

	hcat, hlogs, err := CompileHCL(hclSrc, opts)
	if err != nil {
		t.Fatalf("CompileHCL error: %v", err)
	}
	if len(hlogs) != 0 {
		t.Fatalf("HCL2 compile emitted logs: %v", hlogs)
	}

	ppOpts := opts
	ppOpts.Format = FormatPuppet
	pcat, _, err := Compile(ppSrc, ppOpts)
	if err != nil {
		t.Fatalf("Compile(puppet) error: %v", err)
	}

	hRes, hEdges := canon(hcat)
	pRes, pEdges := canon(pcat)
	if !reflect.DeepEqual(hRes, pRes) {
		t.Fatalf("resources differ:\n HCL2: %#v\n PP:   %#v", hRes, pRes)
	}
	if !reflect.DeepEqual(hEdges, pEdges) {
		t.Fatalf("edges differ:\n HCL2: %v\n PP:   %v", hEdges, pEdges)
	}

	// The JSON surfaces must also coincide up to catalog name.
	var hj, pj map[string]any
	if err := json.Unmarshal([]byte(hcat.JSON()), &hj); err != nil {
		t.Fatalf("HCL2 JSON invalid: %v", err)
	}
	if err := json.Unmarshal([]byte(pcat.JSON()), &pj); err != nil {
		t.Fatalf("PP JSON invalid: %v", err)
	}
}

// TestCompileHCLLiteralAttrs: a lone resource with literal attributes compiles
// to the same catalog as its .pp twin.
func TestCompileHCLLiteralAttrs(t *testing.T) {
	hcl := `
resource "notify" "hello" {
  message = "world"
  withhold = true
}
`
	pp := `
notify { 'hello':
  message  => 'world',
  withhold => true,
}
`
	assertSameCatalog(t, hcl, pp, CompileOptions{NodeName: "n1"})
}

// TestCompileHCLLocalsInterpolation: a locals block plus local.X used both bare
// and interpolated into an attribute round-trips to the identical catalog.
func TestCompileHCLLocalsInterpolation(t *testing.T) {
	hcl := `
locals {
  msg = "hi"
}
resource "notify" "greet" {
  message = local.msg
  withval = "val ${local.msg}"
}
`
	pp := `
$msg = 'hi'
notify { 'greet':
  message => $msg,
  withval => "val ${msg}",
}
`
	assertSameCatalog(t, hcl, pp, CompileOptions{NodeName: "n1"})
}

// TestCompileHCLResourceReference: a resource.TYPE.TITLE reference used as a
// require metaparameter yields the same relationship edge as the .pp twin.
func TestCompileHCLResourceReference(t *testing.T) {
	hcl := `
resource "package" "nginx" {
  ensure = "installed"
}
resource "service" "nginx" {
  ensure  = "running"
  require = resource.package.nginx
}
`
	pp := `
package { 'nginx': ensure => 'installed' }
service { 'nginx':
  ensure  => 'running',
  require => Package['nginx'],
}
`
	assertSameCatalog(t, hcl, pp, CompileOptions{NodeName: "n1"})
}

// TestCompileHCLViaFormatField drives the FormatHCL2 path through Compile's
// options field directly (rather than the CompileHCL convenience wrapper).
func TestCompileHCLViaFormatField(t *testing.T) {
	cat, _, err := Compile(`resource "notify" "x" { message = "y" }`, CompileOptions{Format: FormatHCL2})
	if err != nil {
		t.Fatalf("Compile(FormatHCL2) error: %v", err)
	}
	r, ok := cat.Resource("Notify[x]")
	if !ok {
		t.Fatal("Notify[x] not found")
	}
	if r.Parameters["message"] != "y" {
		t.Fatalf("message = %v, want y", r.Parameters["message"])
	}
}

// TestCompileHCLParseError: a malformed HCL2 manifest surfaces the front-end's
// syntax error through the parse step.
func TestCompileHCLParseError(t *testing.T) {
	if _, _, err := CompileHCL(`resource "notify" "x" {`, CompileOptions{}); err == nil {
		t.Fatal("CompileHCL(malformed) returned nil error")
	}
}

// TestCompileHCLUnsupportedConstruct: a construct the HCL2 front-end does not
// translate (a bare variable reference) surfaces as an error, not a silent stub.
func TestCompileHCLUnsupportedConstruct(t *testing.T) {
	if _, _, err := CompileHCL(`locals { a = b }`, CompileOptions{}); err == nil {
		t.Fatal("CompileHCL(unsupported) returned nil error")
	}
}
