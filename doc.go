// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, the go-ruby-puppet/puppet authors

// Package puppet is a pure-Go (no cgo) adapter that presents the Ruby Puppet
// API surface over the Puppet-language engine github.com/go-puppet/puppet.
//
// The go-puppet engine already implements everything that is Puppet-language
// semantics: lexing and parsing a manifest, evaluating expressions, scoping,
// conditionals and iteration, class and defined-type instantiation, resource
// declaration and relationship chaining, and compiling the result into a
// catalog graph. It delegates the type system to github.com/go-pcore/pcore,
// data binding to github.com/go-hiera/hiera, and node facts to an injectable
// provider. This package does not reimplement any of that; it wraps the engine
// and adds the thin, Ruby-facing conveniences the engine deliberately omits so
// that a consumer such as go-embedded-ruby (rbgo) can expose a Ruby "Puppet"
// class:
//
//   - [Parse], the surface behind a Ruby Puppet::Pops parse check, which
//     reports a manifest's syntax error (or nil) without exposing the AST;
//   - [Compile], which compiles a manifest into a catalog given a node's
//     facts, name and optional Hiera configuration, returning the compiled
//     catalog, the notice/warning/err log lines and any evaluation error;
//   - [Catalog] and [Resource], a Ruby-shaped view of the compiled catalog:
//     resources addressed by their canonical `Type[Title]` reference, edges as
//     flat source/target reference pairs, the catalog size, and the Puppet
//     catalog JSON;
//   - [CompileOptions], carrying the per-node compile context (facts, node
//     name, Hiera configuration path and interpolation scope).
//
// The package has no dependency on any Ruby runtime: the surface is Go-typed,
// and a Ruby binding layer marshals Ruby values onto these Go types. Being
// pure Go, it cross-compiles to and runs on every 64-bit Go target and links
// into a static binary by default.
package puppet
