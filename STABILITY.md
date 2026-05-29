# Stability & Compatibility Policy

Starting at **v1.0.0**, Pulse adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). This document explains exactly what that means in practice — what we promise, what we don't, and how breaking changes are introduced.

## 1. Public API surface

The **public API** consists of every exported identifier (types, functions, methods, constants, variables) in:

- `github.com/MUKE-coder/pulse/pulse`
- `github.com/MUKE-coder/pulse/ui` (the `DistFS()` accessor only — embedded asset contents are not part of the API)

Every symbol with a leading capital letter in those packages is covered by the stability guarantees below.

### Out of scope

The following are **not** part of the stable surface and may change in any release, including patch releases:

- Unexported identifiers (lowercase symbols).
- The on-wire JSON schema of `/pulse/api/*` endpoints. The endpoints themselves are stable; the field set may grow. New fields are additive — clients should ignore unknown fields.
- The HTML / JS / CSS content of the embedded dashboard.
- The SQL schema of the SQLite storage backend. Migrations are handled transparently on `NewSQLiteStorage`; consuming the database file with external SQL tooling is not supported.
- The dependency tree (Go module versions).
- The set of background goroutines started by `Mount`.
- Log output format and verbosity.
- Default values that are documented as "subject to change" (e.g., USE colour bands).

If you need stability on something currently out of scope, open an issue describing the use case.

## 2. Guarantees

### Patch releases (`v1.0.0` → `v1.0.1`)

- **No source-level breaking changes** to the public API.
- Bug fixes, security fixes, performance improvements.
- New deprecation notices may be added (but the deprecated symbol keeps working).

### Minor releases (`v1.0.0` → `v1.1.0`)

- **No source-level breaking changes** to the public API.
- New exported types, functions, methods, options.
- New API endpoints.
- New configuration knobs (with backwards-compatible defaults).
- New deprecation notices.
- Existing deprecated symbols are **not** removed in a minor release.

### Major releases (`v1.0.0` → `v2.0.0`)

- May contain breaking changes to the public API.
- Will not be released without at least one prior minor release containing the deprecation notice for every removed symbol.
- Will ship with a written migration guide in `CHANGELOG.md`.
- New major versions live on a separate module path (`github.com/MUKE-coder/pulse/v2`) so consumers can upgrade at their own pace, per the [Go module versioning rules](https://go.dev/blog/v2-go-modules).

## 3. Deprecation policy

When a public symbol needs to be removed or replaced, the process is:

1. The symbol is marked **deprecated** in its godoc comment, with a `// Deprecated: …` line pointing at the replacement, in a **minor** release.
   ```go
   // OldThing does the old thing.
   //
   // Deprecated: use NewThing instead — see CHANGELOG.md#vX.Y.0.
   func OldThing() { … }
   ```
2. The deprecated symbol keeps working unchanged for **at least one full minor release cycle**.
3. The symbol is removed only in the next **major** release.

Go's tooling (`go vet`, `staticcheck`, IDEs) flags `// Deprecated:` symbols at the call site, so consumers see the warning long before the symbol disappears.

### Exceptions

There are two cases where we may break the API outside a major release:

- **Security fixes.** A critical security vulnerability may require a breaking change in a patch release. We'll cut a major version as soon as possible afterwards, and the patch will be clearly documented.
- **Severe correctness bugs.** A bug whose only safe fix is a signature change. This has never happened. If it ever does, the patch will document it loudly.

We expect both exceptions to be rare. The bar is "is this worse than asking everyone to recompile?" — we err on the side of stability.

## 4. Support

- **Latest stable major version** — actively developed; receives features, fixes, and security patches.
- **Previous major version** — receives security patches for 6 months after the next major ships.
- **All older versions** — best-effort only; pin to a tag and bring your own backports.

## 5. Reporting incompatibilities

If you hit a compile or runtime break upgrading between non-major versions, that's a bug. Please open an issue with:

- The Pulse version you upgraded from.
- The Pulse version you upgraded to.
- The exact compiler error or runtime symptom.
- A minimal reproducer if possible.

We treat unintentional breakage as a release blocker for the next patch.

## 6. Version constant

The current SDK version is available at runtime as the `pulse.Version` constant. It matches the Git tag exactly (no `v` prefix).

```go
fmt.Printf("Pulse SDK %s\n", pulse.Version)
```

---

**Effective from:** `v1.0.0`.
**Document version:** 1.0.
