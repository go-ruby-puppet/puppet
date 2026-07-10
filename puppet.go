// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-puppet/puppet authors

package puppet

import (
	gohiera "github.com/go-hiera/hiera"
	gopuppet "github.com/go-puppet/puppet"
	"github.com/go-puppet/puppet/ast"
	"github.com/go-puppet/puppet/catalog"
	"github.com/go-puppet/puppet/eval"
	"github.com/go-puppet/puppet/hcl"
)

// LogEntry re-exports the engine's notice/warning/err log line so a Ruby
// binding can surface Puppet log output without importing the engine directly.
type LogEntry = eval.LogEntry

// Parse parses a manifest and reports a syntax error, if any. It is the surface
// behind a Ruby Puppet::Pops parse check: it discards the resulting AST and
// returns only whether the manifest is syntactically valid.
func Parse(manifest string) error {
	_, err := gopuppet.Parse(manifest)
	return err
}

// Resource is one compiled catalog resource: a typed, titled bag of parameters
// together with its tags. It is the Ruby-shaped view of a go-puppet catalog
// resource.
type Resource struct {
	Type       string
	Title      string
	Parameters map[string]any
	Tags       []string
}

// Ref returns the canonical Puppet reference `Type[Title]`.
func (r *Resource) Ref() string { return r.Type + "[" + r.Title + "]" }

// Catalog wraps a compiled go-puppet catalog with a Ruby-shaped surface.
type Catalog struct {
	cat *catalog.Catalog
}

// Resources returns the compiled resources in declaration order.
func (c *Catalog) Resources() []*Resource {
	src := c.cat.Resources()
	out := make([]*Resource, len(src))
	for i, r := range src {
		out[i] = convertResource(r)
	}
	return out
}

// Resource returns the resource for ref (`Type[Title]`) and whether it exists.
func (c *Catalog) Resource(ref string) (*Resource, bool) {
	r, ok := c.cat.Get(ref)
	if !ok {
		return nil, false
	}
	return convertResource(r), true
}

// Edges returns the catalog's directed relationships as flat
// [source ref, target ref] pairs, in insertion order.
func (c *Catalog) Edges() [][2]string {
	src := c.cat.Edges()
	out := make([][2]string, len(src))
	for i, e := range src {
		out[i] = [2]string{e.Source, e.Target}
	}
	return out
}

// JSON renders the catalog in the Puppet catalog JSON shape.
func (c *Catalog) JSON() string { return c.cat.JSON() }

// Size reports the number of resources in the catalog.
func (c *Catalog) Size() int { return len(c.cat.Resources()) }

// convertResource maps an engine catalog resource to the Ruby-shaped Resource.
func convertResource(r *catalog.Resource) *Resource {
	return &Resource{
		Type:       r.Type,
		Title:      r.Title,
		Parameters: r.Parameters,
		Tags:       r.Tags,
	}
}

// mapFacts adapts a plain facts map to the engine's [eval.FactsProvider].
type mapFacts map[string]any

// Fact returns the value bound to name and whether it is present.
func (m mapFacts) Fact(name string) (eval.Value, bool) {
	v, ok := m[name]
	return v, ok
}

// Facts returns the whole facts map (exposed to manifests as `$facts`).
func (m mapFacts) Facts() map[string]any { return map[string]any(m) }

// Format selects the surface syntax a manifest is written in. Both formats
// parse to the same [ast.Program] and evaluate through the identical engine, so
// they produce identical catalogs.
type Format int

const (
	// FormatPuppet is native Puppet (.pp) source. It is the zero value, so a
	// CompileOptions with no Format set stays a Puppet compile.
	FormatPuppet Format = iota
	// FormatHCL2 is Terraform-style HCL2 source, translated to the same AST by
	// the go-puppet hcl front-end.
	FormatHCL2
)

// parseManifest parses a manifest into a program using the front-end selected
// by format. It is the single seam where Puppet and HCL2 diverge; every later
// step (evaluate → catalog) is shared.
func parseManifest(manifest string, format Format) (*ast.Program, error) {
	if format == FormatHCL2 {
		return hcl.Parse(manifest)
	}
	return gopuppet.Parse(manifest)
}

// CompileOptions carries the node context for a compile.
type CompileOptions struct {
	// Format is the surface syntax of the manifest (default FormatPuppet).
	Format Format
	// Facts are the node facts, also exposed to manifests via $facts and the
	// engine's FactsProvider.
	Facts map[string]any
	// NodeName is the compiling node's name (default "default").
	NodeName string
	// HieraConfig is an optional path to a hiera.yaml; when set, a Hiera engine
	// is loaded and wired for data binding and lookup().
	HieraConfig string
	// HieraScope is an optional scope for Hiera interpolation; when nil a
	// MapScope is derived from Facts.
	HieraScope gohiera.Scope
}

// Compile compiles a manifest into a catalog for the node described by opts,
// returning the catalog, the emitted log lines, and any evaluation error.
//
// It always wires the facts provider and node name. When opts.HieraConfig is
// set it loads a Hiera engine from that hiera.yaml — using opts.HieraScope, or
// a MapScope built from opts.Facts when the scope is nil — and wires it for
// data binding; a Hiera load error is returned before evaluation.
func Compile(manifest string, opts CompileOptions) (*Catalog, []LogEntry, error) {
	name := opts.NodeName
	if name == "" {
		name = "default"
	}

	evalOpts := []eval.Option{
		eval.WithFacts(mapFacts(opts.Facts)),
		eval.WithNodeName(name),
	}

	if opts.HieraConfig != "" {
		scope := opts.HieraScope
		if scope == nil {
			scope = factsScope(opts.Facts)
		}
		h, err := gohiera.Load(opts.HieraConfig, scope)
		if err != nil {
			return nil, nil, err
		}
		evalOpts = append(evalOpts, eval.WithHiera(h))
	}

	prog, err := parseManifest(manifest, opts.Format)
	if err != nil {
		return nil, nil, err
	}
	e := eval.New(evalOpts...)
	cat, err := e.EvalProgram(prog)
	logs := e.Logs()
	if err != nil {
		return nil, logs, err
	}
	return &Catalog{cat: cat}, logs, nil
}

// CompileHCL compiles a Terraform-style HCL2 manifest into a catalog. It is a
// convenience wrapper that forces opts.Format to FormatHCL2 before delegating to
// Compile; the resulting catalog is identical to the one the twin Puppet source
// would produce.
func CompileHCL(manifest string, opts CompileOptions) (*Catalog, []LogEntry, error) {
	opts.Format = FormatHCL2
	return Compile(manifest, opts)
}

// factsScope builds a Hiera MapScope from a facts map, placing every fact at
// top level and the whole map under a "facts" key so both legacy
// (%{osfamily}) and namespaced (%{facts.os.family}) interpolation resolve.
func factsScope(facts map[string]any) gohiera.Scope {
	scope := gohiera.MapScope{}
	for k, v := range facts {
		scope[k] = v
	}
	scope["facts"] = map[string]any(facts)
	return scope
}
