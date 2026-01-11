# Agent Development Guidelines

Project-specific guidelines for AI-assisted development of the GitHub Repository Token Issuer App.

## Project Philosophy

**Core Principle**: Secure, cost-effective, and simple. This is a single-purpose utility that issues GitHub tokens. Resist feature creep and over-engineering at all costs.

### Priorities (in order)

1. **Security** (but not too restrictive - enable necessary workflows)
2. **Costs**
3. **Simplicity**

### Architectural Decisions

1. **Stateless** - No database or persistent storage, all validation happens per-request
2. **Fail Fast** - No retries, no fallbacks, immediate error responses
3. **No Caching** - Fetch fresh data from Secret Manager and GitHub API on every request
4. **No Observability** - No logging, no metrics, no monitoring (intentional cost/complexity reduction)

## Technology Stack

- **Language**: Go 1.25+ (use 1.25.* in documentation)
- **Platform**: Google Cloud Run (2nd generation)
- **IaC**: Terraform (single main.tf file, GCS backend with locking)
- **CI/CD**: GitHub Actions (lint → terraform plan → deploy)
- **Libraries**:
  - `google/go-github` for GitHub API
  - `golang-jwt/jwt` or go-github's JWT methods
  - GCP Go SDK for Secret Manager

## Code Organization

```
function/     # All Go code
terraform/    # All infrastructure code
action.yml    # Composite action in root
.github/workflows/deploy.yml  # CI/CD
```

**Never** create files in repository root except:
- `action.yml` (already exists)
- Documentation (README.md, DEVELOPMENT.md, AGENT.md, CLAUDE.md)
- Standard files (.gitignore, LICENSE, etc.)

## Documentation Standards

- **README.md**: User-facing only (overview, usage, error codes, repo structure)
- **DEVELOPMENT.md**: Technical details (architecture, implementation, local dev, deployment)
- **AGENT.md**: AI agent development guidelines (this file)
- **CLAUDE.md**: Main entry point that includes AGENT.md
- Keep Table of Contents updated in README.md and DEVELOPMENT.md
- No emojis unless explicitly requested
- Use GitHub-flavored markdown

## Security Rules (NEVER violate)

1. **Repository permissions only** - Never add organization or account-level permissions
2. **Read-only security scopes** - These must stay read-only:
   - `code_scanning`
   - `dependabot_alerts`
   - `security_advisories`
   - `secret_scanning`
3. **No logging** of:
   - OIDC tokens
   - GitHub App private keys
   - Installation access tokens
   - JWT tokens
4. **Duplicate scope rejection** - Always return 400 if same scope appears multiple times

## What NOT to Add (Unless Explicitly Requested)

- ❌ Logging or monitoring
- ❌ Caching (tokens, Secret Manager responses, etc.)
- ❌ Retries or fallback logic
- ❌ Request deduplication
- ❌ Health check endpoints
- ❌ Metrics or observability
- ❌ Token revocation
- ❌ Custom token expiration
- ❌ Organization permissions
- ❌ Testing infrastructure (mentioned as "will think about later")
- ❌ Documentation beyond README.md, DEVELOPMENT.md, AGENT.md, CLAUDE.md

## Code Style

- **Error handling**: Fail fast, return errors immediately
- **Comments**: Only where logic isn't self-evident
- **Validation**: At system boundaries only (user input, external APIs)
- **Abstractions**: Avoid creating them for one-time operations
- **Configuration**: Environment variables for runtime, hardcoded Go constants for scopes

## Common Tasks

### Adding a New Repository Permission Scope

1. Update `function/scopes.go` with scope ID and allowed levels
2. Update README.md Allowed Scopes table
3. Update DEVELOPMENT.md if needed
4. Test and deploy via CI/CD

### Modifying API Behavior

- API is intentionally simple: single POST /token endpoint with query params
- Don't add new endpoints or change request/response format without explicit user request

### Updating Documentation

- README changes: Update Table of Contents if adding/removing sections
- Technical details go in DEVELOPMENT.md, not README
- Keep examples accurate and tested

## Deployment Approach

- **Changes to function/**: Requires terraform apply (rebuilds container)
- **Changes to terraform/**: Run terraform plan first, then apply
- **CI/CD**: Triggered on push to main, runs lint → plan → deploy
- **No canary/blue-green** except for key rotation

## Scope Management

**Allowlist** (in `function/scopes.go`):
- 25 repository permission scopes
- Map of scope_id → []string{"read", "write"} or []string{"read"}
- Security scopes are read-only

**Blacklist**: Currently empty, can be used to block specific scopes

**Validation order**:
1. Check for duplicates → 400
2. Check blacklist → 400
3. Check allowlist → 400
4. Verify permission levels → 400
5. Query GitHub for granted permissions → 403 if insufficient
6. Request token → 403 if GitHub returns fewer scopes than requested

## GitHub Actions Integration

- Composite action at `./action.yml`
- Input: `scopes` (multiline format, one scope:permission per line)
- Output: `token`
- Requires `permissions: id-token: write` in workflow

## When Modifying This Project

1. **Read first** - Never propose changes to code you haven't read
2. **Stay minimal** - Only make changes directly requested or clearly necessary
3. **No "improvements"** - Don't refactor, don't add error handling for impossible scenarios
4. **Test assumptions** - If unclear, use AskUserQuestion
5. **Update both docs** - README.md and DEVELOPMENT.md must stay in sync

## Error Response Format

Always return JSON errors with this structure:
```json
{
  "error": "Human-readable message",
  "details": { /* optional context */ }
}
```

Standard status codes: 400, 401, 403, 500, 503 (see DEVELOPMENT.md for mappings)

## API Design Principles

### Single Endpoint Philosophy

- **One endpoint**: POST /token
- **Query parameters for scopes**: `?scope_id=permission&scope_id=permission`
- **No path parameters**: No `/repos/{owner}/{repo}` style paths
- **No path validation**: Don't validate repository in URL against OIDC claims
- **OIDC in header**: Authorization: Bearer <GITHUB_OIDC_TOKEN>

### Request Format

**Query Parameters**:
```
?contents=write&deployments=write&statuses=write
```

**Headers**:
```
Authorization: Bearer <GITHUB_OIDC_TOKEN>
```

**No request body** - all parameters in query string

### Response Format

**Success (200)**:
```json
{
  "token": "ghs_...",
  "expires_at": "2026-01-11T13:00:00Z",
  "scopes": {
    "contents": "write",
    "deployments": "write"
  }
}
```

**Error (4xx/5xx)**:
```json
{
  "error": "Human-readable message",
  "details": { /* optional */ }
}
```

## Implementation Details

### OIDC Token Handling

- **GCP IAM validates** signature, issuer, audience, expiration
- **Function extracts** repository claim only
- **No additional verification** needed in function code

### Scope Parsing

```go
// Parse query parameters
scopes := make(map[string]string)
for param, values := range r.URL.Query() {
    if len(values) > 1 {
        return fmt.Errorf("duplicate scope '%s' in request", param)
    }
    permission := values[0]
    if permission != "read" && permission != "write" {
        return fmt.Errorf("invalid permission '%s' for scope '%s'", permission, param)
    }
    scopes[param] = permission
}
```

### JWT Creation

- **Algorithm**: RS256
- **Expiration**: 10 minutes (GitHub's maximum)
- **Claims**: iat, exp, iss (GitHub App ID)
- **Signing**: Use private key from Secret Manager

### Installation Token Request

- Request token with exact scopes
- Fixed 1-hour expiration
- Verify granted scopes match requested scopes exactly
- Return 403 if GitHub returns fewer scopes than requested

## File Structure

### function/

```
function/
├── main.go        # HTTP server, routing, startup validation
├── handlers.go    # TokenHandler, query param parsing, response formatting
├── validation.go  # ValidateScopes, ExtractRepositoryFromOIDC, duplicate detection
├── scopes.go      # AllowedScopes map, BlacklistedScopes set
├── github.go      # GitHub API client, JWT creation, token issuance
├── go.mod         # Dependencies
├── go.sum         # Checksums
└── Dockerfile     # Multi-stage build (golang:1.25 → distroless)
```

### terraform/

```
terraform/
├── main.tf      # All GCP resources (Cloud Run, Secret Manager, IAM)
├── variables.tf # Input variables (project_id, region, github_app_id)
└── outputs.tf   # Output values (Cloud Run URL)
```

## Future Work

**Testing**: User mentioned "will think about testing later" - don't add test infrastructure proactively

**Monitoring**: Intentionally omitted for simplicity - don't add unless explicitly requested

**Performance**: Current design (no caching, fetch every request) is intentional for simplicity

## Version Information

- **Go Version**: Always use 1.25+ or 1.25.* in documentation
- **Dockerfile Base**: golang:1.25 for build stage
- **Cloud Run**: 2nd generation
- **Terraform**: Latest stable version
