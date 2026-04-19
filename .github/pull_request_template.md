<!--
Thanks for contributing. Please fill in the sections below. Remove any
that genuinely don't apply, but most PRs touch at least Summary + Test
plan + Checklist.

For larger or design-sensitive changes, please open an issue or a
discussion first to align on shape — generator output changes, new
annotations, and runtime API additions in particular.
-->

## Summary

<!-- One paragraph: what changed and why. Link to the issue if there is one. -->

## Type of change

<!-- Check all that apply. -->

- [ ] Bug fix (non-breaking)
- [ ] New feature (non-breaking)
- [ ] Generator output changes (may require downstream regeneration)
- [ ] Breaking change to a public API
- [ ] Docs / examples / CI only

## Test plan

<!--
How was this validated? Paste commands and, if helpful, a snippet of
output. For changes that touch generated code, include a sample of the
diff in `*.mcp.pb.go`.
-->

- [ ] `make test` passes locally
- [ ] `make lint` passes locally
- [ ] `make gen` leaves no diff (if proto / generator changed)
- [ ] New tests added (or a reason not to)

## Design principles checklist

<!-- See CONTRIBUTING.md + AGENTS.md. Tick everything that applies. -->

- [ ] Default-deny rendering is preserved (no auto-expose)
- [ ] `modelcontextprotocol/go-sdk` remains the sole MCP protocol layer (no hand-rolled JSON-RPC, session, progress, etc.)
- [ ] `OUTPUT_ONLY` runtime clear is preserved (recursion through nested / repeated / map)
- [ ] Client-streaming / bidi RPCs still fail at codegen (not silently skipped)
- [ ] Generator output remains deterministic (no timestamps, random IDs, map iteration leakage)
- [ ] Annotation schema changes are backward-compatible (no tag renumbering, no removed fields)

## Notes for reviewers

<!-- Anything subtle worth pointing out — tradeoffs, follow-ups, open questions. -->
