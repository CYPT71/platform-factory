# Contributing to platform-factory

Thank you for your interest in contributing to platform-factory! This document provides
guidelines for contributing to the project.

## Getting Started

### Prerequisites

- Go 1.21 or later
- Docker (for container-based development)
- Podman (optional, for MicroVM testing)
- Git
- GNU Make

### Setting Up the Development Environment

1. **Clone the repository**:
   ```bash
   git clone https://github.com/CYPT71/platform-factory.git
   cd platform-factory-base
   ```

2. **Install dependencies**:
   ```bash
   go mod download
   ```

3. **Build the project**:
   ```bash
   go build ./...
   ```

4. **Run tests**:
   ```bash
   go test ./... -short -race
   ```

## Code Style

### Go Style

- Follow [Effective Go](https://go.dev/doc/effective_go) guidelines
- Use `gofmt` to format your code:
  ```bash
  gofmt -w .
  ```
- Use `golint` for additional style checks
- Prefer descriptive variable and function names
- Use camelCase for variable names, PascalCase for exported identifiers

### Formatting

- Use 4 spaces for indentation (tabs are converted automatically by `gofmt`)
- Keep line length under 100 characters when possible
- Use consistent spacing around operators and after commas

### Comments

- Every exported function, type, and constant should have a doc comment
- Use complete sentences in doc comments
- Start doc comments with the name of the identifier:
  ```go
  // Package foo implements bar.
  package foo

  // Compile compiles the source code.
  func Compile() error {
  }
  ```

## Pull Requests

### Before Submitting

1. Ensure all tests pass:
   ```bash
   go test ./... -short -race
   ```

2. Ensure code coverage is maintained at 87% or higher:
   ```bash
   go test ./... -short -race -coverprofile=coverage.out
   go tool cover -func=coverage.out
   ```

3. Run the linter:
   ```bash
   golangci-lint run
   ```

4. Ensure your code builds without errors

5. Squash related commits into a single, well-described commit

### Pull Request Template

Use the following template for your pull request:

```markdown
## Description

Brief description of the change and the problem it solves.

## Related Issues

- Fixes #123
- Related to #456

## Changes Made

- Changed X to do Y
- Added Z to support W

## Testing

- All existing tests pass
- Added new tests for the following cases:
  - Case 1
  - Case 2

## Checklist

- [x] Code follows project style guidelines
- [x] All tests pass
- [x] Code coverage >= 87%
- [x] Documentation updated (if applicable)
- [x] Compatible with existing API
- [x] Security considerations addressed
```

### Review Process

1. **Triage**: A maintainer will review your PR within 24-48 hours
2. **Feedback**: You may receive feedback requesting changes
3. **Approval**: Once approved, a maintainer will merge your PR
4. **CI**: All CI checks must pass before merging

### Required Approvals

- **1 approval**: Standard changes
- **2+ approvals**: Changes to critical components (pipeline, executor, sandbox, signing, mTLS)
- **Security team approval**: Changes affecting security or cryptography

## Testing

### Unit Tests

- Write unit tests for all new functionality
- Test both success and error cases
- Use table-driven tests for similar test cases:
  ```go
  func TestSomething(t *testing.T) {
      tests := []struct {
          name     string
          input    Input
          expected Output
      }{
          {"case 1", Input{...}, Output{...}},
          {"case 2", Input{...}, Output{...}},
      }
      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              // test logic
          })
      }
  }
  ```

### Integration Tests

- Add integration tests in the `tests/` directory
- Use the `TestMain` pattern for setup/teardown:
  ```go
  func TestMain(m *testing.M) {
      // setup
      defer cleanup()
      os.Exit(m.Run())
  }
  ```

### Race Detection

- Always run tests with the race detector:
  ```bash
  go test -race ./...
  ```

### Coverage

- Aim for 100% coverage in critical packages
- Minimum 87% coverage for all packages
- Use `go test -coverprofile` to generate coverage reports

## Commit Messages

Use the following format for commit messages:

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Types

- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Test-related changes
- `chore`: Build process or auxiliary tool changes
- `perf`: Performance improvements
- `security`: Security-related changes

### Scope

The scope should be the package or component affected by the change:
- `pipeline`
- `executor`
- `signing`
- `oci`
- `microvm`
- `sandbox`
- etc.

### Subject

- Use imperative mood ("Add" not "Adds" or "Added")
- Keep it concise (50 characters or less)
- Capitalize the first letter
- No period at the end

### Body

- Explain what was changed and why
- Reference related issues or PRs
- Include breaking changes if applicable

### Footer

- Reference related issues: `Fixes #123`
- Reference breaking changes: `BREAKING CHANGE: ...`

### Examples

```
feat(pipeline): add parallel stage execution

Implement parallel execution of pipeline stages that have no
dependencies on each other. This improves build times for projects
with independent build steps.

Fixes #456
```

```
fix(signing): correct signature verification

Fix a bug where signature verification could succeed with an
empty signature. Added explicit check for empty signatures.

Fixes #789
```

## Coding Standards

### Error Handling

- Always check errors:
  ```go
  data, err := readFile()
  if err != nil {
      return fmt.Errorf("read failed: %w", err)
  }
  ```

- Use `fmt.Errorf` with `%w` for wrapping errors:
  ```go
  return fmt.Errorf("operation failed: %w", err)
  ```

- Define custom error types for expected error conditions:
  ```go
  var ErrNotFound = errors.New("not found")
  ```

### Concurrency

- Use `sync.WaitGroup` for coordinating goroutines
- Always use mutexes or channels for shared state
- Prefer channels for communication between goroutines
- Use `context.Context` for cancellation and timeouts

### Security

- Never use user input directly in shell commands
- Always validate and sanitize inputs
- Use constant-time comparisons for secrets
- Never log sensitive information
- Use cryptographically secure random number generation

### Performance

- Preallocate slices when the size is known:
  ```go
  results := make([]Result, 0, 100)
  ```

- Use `sync.Pool` for frequently allocated objects
- Avoid unnecessary allocations in hot paths
- Use `bytes.Buffer` for string concatenation in loops

## Documentation

### Code Documentation

- Every exported identifier should have a doc comment
- Use examples in doc comments when helpful:
  ```go
  // Compile compiles the source code and returns the output path.
  //
  // Example:
  //
  //   output, err := Compile("source.go")
  //   if err != nil {
  //       log.Fatal(err)
  //   }
  func Compile(source string) (string, error) {
  }
  ```

### Architecture Documentation

- Update architecture documents in `.wiki-worktree/` when making architectural changes
- Document design decisions in ADR (Architecture Decision Record) files
- Update diagrams when interfaces or workflows change

## Versioning

The project uses semantic versioning (SemVer):

- **MAJOR**: Breaking changes, incompatible API changes
- **MINOR**: Backward-compatible new features
- **PATCH**: Backward-compatible bug fixes

### Backward Compatibility

- Breaking changes require a major version bump
- New features that are backward-compatible can be added in minor versions
- Bug fixes that are backward-compatible can be added in patch versions
- Deprecations require a minor version bump and must include migration guidance

## Releases

### Release Process

1. Create a release branch: `git checkout -b release/vX.Y.Z`
2. Update version numbers in code and documentation
3. Update CHANGELOG.md with release notes
4. Create a PR for the release branch
5. Once approved, tag the release: `git tag -s vX.Y.Z`
6. Push the tag: `git push origin vX.Y.Z`
7. Publish release artifacts

### Release Checklist

- [ ] All tests pass
- [ ] Code coverage >= 87%
- [ ] All critical issues for the release are resolved
- [ ] CHANGELOG.md is updated
- [ ] Documentation is updated
- [ ] Upgrade guide is written (if breaking changes)
- [ ] Release is signed and verified

## Security

### Reporting Security Issues

**Do not report security issues in public GitHub issues.**

Instead, email the security team at `security@platform-factory.dev` with:
- A description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if available)

### Security Best Practices

- Never commit secrets to the repository
- Use environment variables or secret management systems for credentials
- Rotate credentials regularly
- Use principle of least privilege
- Audit code changes for security implications

## Community

### Communication

- **GitHub Discussions**: For general questions and discussions
- **GitHub Issues**: For bug reports and feature requests
- **Email**: For private matters (info@platform-factory.dev)
- **Security**: For security vulnerabilities (security@platform-factory.dev)

### Code of Conduct

All contributors are expected to follow our [Code of Conduct](CODE_OF_CONDUCT.md).
Report violations to conduct@platform-factory.dev.

### Becoming a Maintainer

See [MAINTAINERS.md](MAINTAINERS.md) for information on becoming a maintainer.

## Resources

- [Go Documentation](https://go.dev/doc/)
- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [OCI Specification](https://github.com/opencontainers/image-spec)

## License

By contributing to this project, you agree to license your contribution under the
[Apache License 2.0](LICENSE).
