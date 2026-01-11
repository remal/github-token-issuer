# Development Documentation

Technical implementation details and architecture documentation for the GitHub Repository Token Issuer App.

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Code Structure](#code-structure)
- [Implementation Details](#implementation-details)
- [Security Considerations](#security-considerations)
- [Local Development](#local-development)
- [Deployment](#deployment)
- [Adding New Scopes](#adding-new-scopes)
- [Troubleshooting](#troubleshooting)

## Architecture Overview

### High-Level Design

The app is a stateless Cloud Run Function that acts as a broker between GitHub Actions workflows and GitHub's installation token API. It validates requests, checks permissions, and issues short-lived tokens.

**Priorities (in order):**

1. **Security** (but not too restrictive - enable necessary workflows)
2. **Costs**
3. **Simplicity**

**Architectural Decisions:**

1. **Stateless** - No database or persistent storage, all validation happens per-request
2. **Fail Fast** - Errors are returned immediately without retries to keep logic simple
3. **No Caching** - Fetch fresh data from Secret Manager and GitHub API on every request to avoid stale data
4. **No Observability** - No logging, no metrics, no monitoring (intentional cost/complexity reduction)

### Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│ GitHub Actions Workflow                                      │
│                                                              │
│  1. Obtain OIDC token from GitHub                           │
│  2. Call Cloud Run Function with OIDC token as IAM bearer   │
│     POST /token?contents=write&deployments=write            │
│     Authorization: Bearer <GITHUB_OIDC_TOKEN>               │
└─────────────────────┬───────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────┐
│ GCP Cloud Run Function (Go)                                  │
│                                                              │
│  1. GCP IAM validates GitHub OIDC token                      │
│  2. Extract repository claim from OIDC token                 │
│  3. Parse scope query parameters                             │
│  4. Validate scopes against allowlist/blacklist              │
│  5. Fetch GitHub App private key from Secret Manager        │
│  6. Create JWT to authenticate as GitHub App                 │
│  7. Fetch App permissions from GitHub API                    │
│  8. Verify repo has App installed                            │
│  9. Verify requested scopes don't exceed granted permissions │
│ 10. Create installation token via GitHub API                 │
│ 11. Return token with metadata                               │
└─────────────────────┬───────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────┐
│ GCP Secret Manager                                           │
│  - GitHub App Private Key (PEM format)                       │
└─────────────────────────────────────────────────────────────┘
```

### Request Flow Details

```
1. GitHub Actions Workflow
   └─> Obtains OIDC token via ACTIONS_ID_TOKEN_REQUEST_URL
   └─> Calls Cloud Run Function with OIDC token as Authorization header

2. Cloud Run IAM Layer
   └─> Validates OIDC token signature and issuer
   └─> Checks token audience matches Cloud Run service URL
   └─> Verifies token hasn't expired
   └─> Allows request through if valid

3. Function Handler (handlers.go)
   └─> Extracts repository claim from OIDC token
   └─> Parses query parameters for scopes
   └─> Calls validation logic

4. Validation Layer (validation.go)
   └─> Check for duplicate scopes
   └─> Validate against allowlist/blacklist (scopes.go)
   └─> Verify each scope has valid permission level

5. GitHub Client (github.go)
   └─> Fetch GitHub App private key from Secret Manager
   └─> Create JWT signed with private key (10 min expiry)
   └─> Authenticate as GitHub App
   └─> Get installation ID for repository
   └─> Fetch App's granted permissions on installation
   └─> Verify requested scopes don't exceed granted permissions
   └─> Create installation access token (1 hour expiry)
   └─> Return token + metadata

6. Response
   └─> Return JSON with token, expiry, and granted scopes
```

**Request Flow Summary**:

1. GitHub Actions Workflow generates OIDC token and calls Cloud Run Function
2. GCP IAM validates GitHub OIDC token for Cloud Run invocation
3. Function extracts repository from OIDC claims
4. Function parses scope permissions from query parameters
5. Function validates scopes against hardcoded allowlist/blacklist
6. Function fetches GitHub App private key from Secret Manager
7. Function creates JWT (10-minute expiry) to authenticate as GitHub App
8. Function queries GitHub API for App installation and permissions
9. Function creates installation token (1-hour expiry) with requested scopes
10. Function returns token and metadata as JSON response

## Code Structure

### Directory Layout

```
function/               # Go application code
├── main.go            # HTTP server and routing
├── handlers.go        # Request/response handling
├── github.go          # GitHub API client and JWT logic
├── validation.go      # Scope and OIDC validation
├── scopes.go          # Allowlist/blacklist definitions
├── go.mod             # Go module dependencies
└── Dockerfile         # Multi-stage container build

terraform/             # Infrastructure as Code
├── main.tf            # All GCP resources
├── variables.tf       # Input variables
└── outputs.tf         # Output values (Cloud Run URL)
```

### Key Files

#### `function/main.go`

- HTTP server setup (port 8080)
- Routes POST /token to handler
- Startup validation (GITHUB_APP_ID env var)
- Graceful shutdown handling

#### `function/handlers.go`

- `TokenHandler()`: Main request handler
- Query parameter parsing (scope name → permission level)
- OIDC token extraction from Authorization header
- Response formatting (JSON with token + metadata)
- Error response handling (400, 401, 403, 500, 503)

#### `function/validation.go`

- `ValidateScopes()`: Check for duplicates, allowlist/blacklist
- `ExtractRepositoryFromOIDC()`: Parse repository claim
- `ValidateScopePermissions()`: Verify read/write are valid
- Duplicate detection logic

#### `function/scopes.go`

- `AllowedScopes`: Map of scope ID → allowed levels (read, write, or both)
- `BlacklistedScopes`: Set of forbidden scopes
- Read-only restrictions for security scopes (code_scanning, dependabot_alerts, etc.)

#### `function/github.go`

- `NewGitHubClient()`: Initialize go-github SDK client
- `GetPrivateKey()`: Fetch from Secret Manager
- `CreateJWT()`: Sign JWT with private key (RS256)
- `GetInstallationID()`: Lookup installation for repository
- `GetInstallationPermissions()`: Query granted permissions
- `CreateInstallationToken()`: Request token from GitHub API
- `VerifyRequestedScopes()`: Compare requested vs granted

## Implementation Details

### OIDC Token Validation

**GCP IAM handles validation**, so the function receives a pre-validated token. The function only extracts claims:

```go
// Extract repository claim from OIDC token
// Expected format: "owner/repo"
repository := extractClaimFromJWT(token, "repository")
```

**No additional signature verification needed** - GCP IAM already verified:

- Token signature is valid
- Issuer is `https://token.actions.githubusercontent.com`
- Audience matches Cloud Run service URL
- Token hasn't expired

### Scope Parsing from Query Parameters

```go
// Parse ?contents=write&issues=read&deployments=write
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

### Scope Validation Logic

1. **Duplicate Check**: Each scope must appear exactly once in query params
2. **Blacklist Check**: Reject if any scope is in blacklist
3. **Allowlist Check**: Reject if scope not in allowlist
4. **Permission Level Check**: Verify permission (read/write) is allowed for that scope
5. **GitHub Permission Check**: Query GitHub API and verify App has required permissions on installation

### JWT Creation for GitHub App Authentication

```go
// JWT claims
claims := jwt.MapClaims{
    "iat": time.Now().Unix(),
    "exp": time.Now().Add(10 * time.Minute).Unix(), // GitHub max
    "iss": os.Getenv("GITHUB_APP_ID"),
}

// Sign with RS256 using private key
token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
signedToken, err := token.SignedString(privateKey)
```

**Important**: JWT must expire within 10 minutes (GitHub's maximum).

### Installation Token Request

```go
// Request installation token with specific scopes
opts := &github.InstallationTokenOptions{
    Permissions: &github.InstallationPermissions{
        Contents:    github.String("write"),
        Deployments: github.String("write"),
        Statuses:    github.String("write"),
    },
}

token, _, err := client.Apps.CreateInstallationToken(ctx, installationID, opts)
```

**GitHub returns**:

- Token string (ghs_...)
- Expiration timestamp (1 hour from creation)
- Actually granted permissions

**Critical validation**: If granted permissions < requested permissions, return 403 error.

### Error Handling Strategy

**Fail Fast Philosophy**: Return errors immediately without retries.

| Error                    | Status | When                              | Action                      |
|--------------------------|--------|-----------------------------------|-----------------------------|
| Duplicate scope          | 400    | Same scope appears multiple times | Reject request              |
| Invalid scope            | 400    | Scope not in allowlist            | Reject request              |
| Blacklisted scope        | 400    | Scope in blacklist                | Reject request              |
| Invalid OIDC             | 401    | GCP IAM rejection                 | Should never reach function |
| App not installed        | 403    | GitHub App not on repo            | Reject request              |
| Insufficient permissions | 403    | App lacks permission              | Reject request              |
| Secret Manager error     | 500    | Can't fetch private key           | Reject request              |
| GitHub API error         | 503    | GitHub unavailable                | Reject request              |

**No retries because**:

- Keeps code simple
- Errors are usually fatal (wrong config, not transient)
- GitHub Actions has built-in retry logic
- Faster failure detection

## Security Considerations

### Private Key Security

**Storage**: GitHub App private key stored in GCP Secret Manager

- Encrypted at rest
- Access controlled via IAM
- Audit logs for all access

**Usage**:

- Fetched on every request (no in-memory caching)
- Never logged or exposed in responses
- Used only to sign JWTs

**Rotation**:

1. Generate new key on GitHub
2. Update Secret Manager secret
3. Deploy new function version
4. Switch traffic
5. Revoke old key

### Read-Only Security Scopes

These scopes are intentionally restricted to read-only to prevent security risks:

- `code_scanning` - Prevents tampering with security alerts
- `dependabot_alerts` - Prevents hiding vulnerabilities
- `security_advisories` - Prevents modifying security disclosures
- `secret_scanning` - Prevents hiding leaked secrets

**Implementation**: Hardcoded in `function/scopes.go`:

```go
AllowedScopes = map[string][]string{
    "code_scanning":       {"read"},
    "dependabot_alerts":   {"read"},
    "security_advisories": {"read"},
    "secret_scanning":     {"read"},
    // ... other scopes with read/write
}
```

### OIDC Token Validation

**GCP IAM validates**:

- Signature validity (RS256)
- Issuer is GitHub Actions
- Audience matches Cloud Run URL
- Token hasn't expired
- Token wasn't revoked

**Function validates**:

- Repository claim exists
- Repository format is valid (owner/repo)

### No Logging of Sensitive Data

**Never log**:

- OIDC token contents
- GitHub App private key
- Installation access tokens
- JWT tokens

**OK to log**:

- Repository name (from claim)
- Requested scopes
- Error types
- Request timestamps

## Technical Specifications

### Runtime Environment

- **Language**: Go 1.25+
- **Platform**: Google Cloud Run (2nd generation)
- **Scaling**:
  - Minimum instances: 0 (cost optimization)
  - Maximum instances: 10 (low volume workload)
  - Cold start latency acceptable

### Dependencies

- **google/go-github SDK**: Official GitHub API client for Go
- **golang-jwt/jwt or go-github JWT methods**: JWT creation and signing
- **GCP Go SDK**: For Secret Manager integration

### Build & Deployment

- **Containerization**: Multi-stage Dockerfile (`function/Dockerfile`)
  - Build stage: golang:1.25+ with full SDK
  - Runtime stage: distroless or scratch for minimal image size
- **CI/CD**: GitHub Actions workflow (.github/workflows/deploy.yml)
  - Triggered on push to main branch
  - Steps: Lint → Terraform plan review → Deploy

## API Reference

### Endpoint

**Single Endpoint**:

```
POST https://github-token-issuer-[hash]-[region].a.run.app/token
```

### Query Parameters

Scopes are specified as query parameters where the parameter name is the **repository permission scope ID** (e.g., `contents`, `issues`, `pull_requests`) and the value is the permission level (`read` or `write`).

**Format**: `?scope_id=permission&scope_id=permission`

**Examples**:

```
# Read access to issues
?issues=read

# Write access to issues and read access to pull requests
?issues=write&pull_requests=read

# Multiple scopes for a deployment workflow
?contents=write&deployments=write&statuses=write
```

**Duplicate Handling**: If the same scope appears multiple times (even with the same permission), the function returns a **400 Bad Request** error.

```
# Invalid - returns 400 error
?issues=read&issues=write
?issues=write&issues=write
```

### Request Headers

```
Authorization: Bearer <GITHUB_OIDC_TOKEN>
```

The GitHub OIDC token serves as both the GCP IAM authentication token and the source of caller identity (repository claim).

### Request Example

```bash
curl -X POST \
  -H "Authorization: Bearer ${GITHUB_OIDC_TOKEN}" \
  "https://github-token-issuer-xyz.run.app/token?issues=write&pull_requests=read"
```

### Response Format

**Success Response (200 OK)**:

```json
{
  "token": "ghs_abc123...",
  "expires_at": "2026-01-11T13:00:00Z",
  "scopes": {
    "contents": "write",
    "deployments": "write",
    "statuses": "write"
  }
}
```

**Response Fields**:

- `token`: The GitHub installation access token (with repository permissions only)
- `expires_at`: ISO 8601 timestamp when token expires (1 hour from issuance)
- `scopes`: Object mapping repository permission scope IDs to granted permission levels

### Error Response Format

```json
{
  "error": "Human-readable error message describing what went wrong",
  "details": {
    "requested_scopes": [
      "contents",
      "deployments",
      "statuses"
    ],
    "granted_scopes": [
      "contents",
      "statuses"
    ],
    "missing_scopes": [
      "deployments"
    ]
  }
}
```

### HTTP Status Codes

| Status Code                   | Scenario                                               | Example                                                               |
|-------------------------------|--------------------------------------------------------|-----------------------------------------------------------------------|
| **200 OK**                    | Success                                                | Token issued with requested scopes                                    |
| **400 Bad Request**           | Duplicate scopes, blacklisted scope, or invalid format | `{"error": "duplicate scope 'issues' in request"}`                    |
| **401 Unauthorized**          | Invalid OIDC token                                     | `{"error": "invalid OIDC token"}`                                     |
| **403 Forbidden**             | App not installed on repo or insufficient permissions  | `{"error": "GitHub App is not installed on repository myorg/myrepo"}` |
| **503 Service Unavailable**   | GitHub API degraded/unavailable                        | `{"error": "GitHub API is temporarily unavailable"}`                  |
| **500 Internal Server Error** | Secret Manager failure, internal errors                | `{"error": "failed to retrieve private key from Secret Manager"}`     |

## Authentication & Security Details

### Authentication Flow

**GitHub OIDC Token as IAM Bearer Token**:

- The GitHub Actions OIDC token is used directly as the Cloud Run IAM bearer token
- GCP IAM validates the token signature and claims
- No additional authentication layer required

### Required OIDC Claims

The function extracts the following claim from the OIDC token:

- **`repository`**: Used to identify which repository the token should be issued for (format: "owner/repo")

### Token Management

#### Installation Token Properties

- **Expiration**: Fixed 1 hour (GitHub's maximum allowed duration)
- **Scope Matching**: Must receive exactly the scopes requested; partial grants are rejected
- **No Caching**: Each request creates a new token; no token reuse across requests

#### JWT Authentication

The function authenticates as the GitHub App using JWT:

- **Algorithm**: RS256 (RSA signature with SHA-256)
- **Expiration**: 10 minutes (GitHub's maximum)
- **Library**: go-github's built-in JWT methods
- **Claims**:
  - `iat`: Issued at timestamp
  - `exp`: Expiration timestamp (iat + 10 minutes)
  - `iss`: GitHub App ID

#### Concurrent Request Handling

**No Coordination Strategy**:

- Each Cloud Run instance handles requests independently
- No token caching or request deduplication
- Each request fetches fresh data from Secret Manager and GitHub API
- Simplicity over optimization; acceptable for low-volume workloads

### Key Rotation Strategy

**Blue-Green Deployment Approach**:

1. Generate new GitHub App private key
2. Update Secret Manager with new key
3. Deploy new function version with reference to new secret
4. Switch Cloud Run traffic to new version
5. Revoke old private key on GitHub

### Scope Management Details

#### Scope Storage and Configuration

**Storage**: Allowed and blacklisted scopes are hardcoded in `function/scopes.go` as constants/maps.

#### Blacklist

**Forbidden scopes** (high-privilege operations that are explicitly blocked):

Currently, all repository permissions at their specified levels are allowed. The blacklist can be customized in `function/scopes.go` to block specific scopes if needed for your security requirements.

#### Validation Logic

1. Parse all scope query parameters (repository permission scope IDs)
2. Check for duplicate scopes → **Reject with 400 if any scope appears more than once**
3. Check if any requested scope is in the blacklist → **Reject entire request (400)**
4. Check if all requested scopes are in the allowlist → **Reject if any scope not allowed (400)**
5. Extract repository from OIDC token and query GitHub API for App's granted repository permissions on that installation
6. Verify each requested scope+permission doesn't exceed App's granted repository permissions
7. Request installation token from GitHub API with exact scopes
8. If GitHub returns fewer scopes than requested → **Fail with error (403)**

#### Scope Validation Rules

When parsing query parameters:

- Each scope must be a valid repository permission scope ID
- Each scope can have either `read` or `write` permission (as specified in the allowed levels)
- Each scope must appear only once; duplicate scopes result in **400 Bad Request**
- Only repository-level permissions are supported; organization or account permissions are not allowed
- This strict validation helps catch misconfigured actions early

### Error Handling Strategy Details

**Failure Handling**:

- **GitHub API Outage**: Fail fast with 503, rely on caller to retry
- **Secret Manager Unavailable**: Fail immediately (no caching or fallback)
- **Archived Repository**: Attempt token issuance anyway; let GitHub API return error if necessary
- **Suspended GitHub App Installation**: Return 403 with clear error message

**Logging Strategy**:

**No Logging**:

- No logs for authentication failures
- No debug logs for requests
- No metrics or observability infrastructure
- Keep implementation simple and cost-minimal

## Infrastructure Details

### GCP Resources (Terraform-managed)

All infrastructure defined in `terraform/main.tf`:

1. **Cloud Run Service**
  - Name: `github-token-issuer`
  - Region: User-configurable (e.g., `us-central1`)
  - Container: Built from `function/Dockerfile`
  - Environment variables: `GITHUB_APP_ID`

2. **Secret Manager Secret**
  - Name: `github-app-private-key`
  - Contains: GitHub App private key in PEM format
  - Access: Cloud Run service account has `secretmanager.secretAccessor` role

3. **Service Account**
  - Name: `github-token-issuer-sa`
  - Purpose: Cloud Run service identity
  - Permissions: Secret Manager access

4. **IAM Bindings**
  - GitHub OIDC federation to invoke Cloud Run
  - Configured to accept tokens with specific `aud` claim
  - Maps GitHub repository claims to Cloud Run invoke permissions

### Terraform State Management

- **Backend**: GCS bucket with state locking
- **Configuration** (in `terraform/main.tf`):
  ```hcl
  terraform {
    backend "gcs" {
      bucket = "your-terraform-state-bucket"
      prefix = "github-token-issuer"
    }
  }
  ```

### Configuration Storage

- **GitHub App ID**: Environment variable `GITHUB_APP_ID` on Cloud Run
- **GitHub App Private Key**: GCP Secret Manager secret `github-app-private-key`
- **Scope Allowlist/Blacklist**: Hardcoded in Go source code (`function/scopes.go`)

### Startup Validation

The function performs the following validation during initialization:

- Check that required environment variables are present (`GITHUB_APP_ID`)
- Fail fast at startup if configuration is invalid

No validation of Secret Manager connectivity or private key format at startup; failures occur on first request.

## Local Development

### Prerequisites

- Go 1.25+
- Docker (for container builds)
- gcloud CLI (for Secret Manager access)
- GitHub App for testing

### Environment Setup

```bash
# Set required environment variables
export GITHUB_APP_ID="your-app-id"
export GOOGLE_CLOUD_PROJECT="your-project-id"

# For local development, authenticate with gcloud
gcloud auth application-default login
```

### Running Locally

```bash
cd function

# Install dependencies
go mod download

# Run the function
go run .

# Function will listen on http://localhost:8080
```

### Testing with curl

```bash
# You'll need a real GitHub OIDC token from a workflow
# or mock the GCP IAM validation layer for local testing

curl -X POST \
  -H "Authorization: Bearer ${GITHUB_OIDC_TOKEN}" \
  "http://localhost:8080/token?contents=write&deployments=write"
```

### Docker Build

```bash
cd function

# Build image
docker build -t github-token-issuer .

# Run container
docker run -p 8080:8080 \
  -e GITHUB_APP_ID="${GITHUB_APP_ID}" \
  -e GOOGLE_CLOUD_PROJECT="${GOOGLE_CLOUD_PROJECT}" \
  github-token-issuer
```

### Linting

```bash
cd function

# Install golangci-lint
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Run linter
golangci-lint run
```

## Deployment

### Prerequisites

1. GCP Project with billing enabled
2. GitHub App created with required permissions
3. GitHub App private key exported as PEM file
4. Terraform installed locally
5. gcloud CLI authenticated

### Initial Setup

1. **Create GitHub App**:
  - Navigate to GitHub Settings → Developer settings → GitHub Apps
  - Configure **Repository permissions** only
  - Do **not** configure Organization permissions or Account permissions
  - Generate private key (download PEM file)
  - Note the App ID
  - Install the app on the repositories where you want to use it

2. **Configure GCP**:
   ```bash
   # Set GCP project
   gcloud config set project YOUR_PROJECT_ID

   # Enable required APIs
   gcloud services enable run.googleapis.com
   gcloud services enable secretmanager.googleapis.com
   gcloud services enable iamcredentials.googleapis.com
   ```

3. **Store GitHub App Private Key**:
   ```bash
   gcloud secrets create github-app-private-key \
     --data-file=path/to/private-key.pem
   ```

4. **Configure Terraform**:
   ```bash
   # Navigate to terraform directory
   cd terraform

   # Initialize Terraform
   terraform init

   # Create terraform.tfvars
   cat > terraform.tfvars <<EOF
   project_id = "your-gcp-project-id"
   region = "us-central1"
   github_app_id = "123456"
   EOF
   ```

5. **Deploy Infrastructure**:
   ```bash
   terraform plan
   terraform apply
   ```

### Terraform Workflow

```bash
cd terraform

# Initialize
terraform init

# Plan changes
terraform plan -var="github_app_id=123456" -var="project_id=my-project"

# Apply
terraform apply
```

### Cloud Run Deployment

**Terraform handles**:

1. Building container from `function/Dockerfile`
2. Pushing to Artifact Registry
3. Deploying to Cloud Run
4. Configuring environment variables
5. Setting up IAM bindings for OIDC

**Manual deployment** (if needed):

```bash
# Build and push
gcloud builds submit function/ --tag gcr.io/PROJECT_ID/github-token-issuer

# Deploy
gcloud run deploy github-token-issuer \
  --image gcr.io/PROJECT_ID/github-token-issuer \
  --region us-central1 \
  --set-env-vars GITHUB_APP_ID=123456 \
  --no-allow-unauthenticated
```

### CI/CD Pipeline

**GitHub Actions workflow** (`.github/workflows/deploy.yml`):

1. **Lint**: Run `golangci-lint` on `function/`
2. **Build**: Compile Go binary to verify build works
3. **Terraform Plan**: Show infrastructure changes
4. **Deploy**: Apply Terraform to deploy new version

**Triggered on**: Push to `main` branch

## Adding New Scopes

### Steps to Add a New Repository Permission Scope

1. **Update `function/scopes.go`**:
   ```go
   AllowedScopes = map[string][]string{
       // ... existing scopes
       "new_scope": {"read", "write"}, // or just {"read"}
   }
   ```

2. **Update README.md** - Add to the Allowed Repository Permission Scopes table:
   ```markdown
   | New Permission Name | `new_scope` | read, write |
   ```

3. **Test the change**:
  - Deploy to test environment
  - Call function with new scope
  - Verify token is issued correctly

4. **Deploy to production** via CI/CD

### Restricting a Scope to Read-Only

To make a scope read-only (like security scopes):

```go
AllowedScopes = map[string][]string{
    "my_scope": {"read"}, // Remove "write"
}
```

This will cause validation to reject `?my_scope=write` with 400 error.

## Troubleshooting

### Common Issues

#### "failed to retrieve private key from Secret Manager"

**Cause**: Cloud Run service account lacks permission to read secret

**Fix**:

```bash
gcloud secrets add-iam-policy-binding github-app-private-key \
  --member="serviceAccount:github-token-issuer-sa@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor"
```

#### "GitHub App is not installed on repository"

**Cause**: GitHub App not installed on the repository making the request

**Fix**: Install the GitHub App on the repository via GitHub settings

#### "insufficient permissions for scope 'X'"

**Cause**: GitHub App doesn't have the requested permission on its installation

**Fix**: Update GitHub App permissions in GitHub App settings, then re-accept installation on repositories

#### "duplicate scope 'X' in request"

**Cause**: Query string has same scope multiple times (e.g., `?issues=read&issues=write`)

**Fix**: Ensure action.yml and workflow only specify each scope once

### Debugging Tips

**Check Cloud Run logs**:

```bash
gcloud logs read --project=PROJECT_ID \
  --resource-type=cloud_run_revision \
  --limit=50
```

**Verify Secret Manager access**:

```bash
gcloud secrets versions access latest --secret=github-app-private-key
```

**Test GitHub App JWT creation**:

```bash
# Create JWT manually and test with GitHub API
curl -H "Authorization: Bearer $(cat jwt.txt)" \
  https://api.github.com/app
```

**Validate OIDC token locally**:

```bash
# Decode token (don't verify signature)
echo "$OIDC_TOKEN" | cut -d. -f2 | base64 -d | jq
```

## Performance Considerations

### Request Latency

**Expected latency**: 500ms - 2s per request

**Breakdown**:

- Secret Manager fetch: 100-300ms
- GitHub API calls (2-3): 200-800ms
- JWT creation: <10ms
- Validation logic: <10ms

**Cold start**: Add 1-3s for container startup (Go is relatively fast)

### Scaling

**Auto-scaling configuration**:

- Min instances: 0 (cost optimization)
- Max instances: 10 (low volume expected)
- Concurrency: 80 requests per instance

**No state to share**: Each instance is independent

### Cost Optimization

**Low cost design**:

- No database
- No caching layer
- Minimal compute (short request duration)
- Pay only for actual requests
- Secret Manager reads are inexpensive

**Estimated cost** (for <100 requests/day):

- Cloud Run: <$1/month
- Secret Manager: <$0.10/month
- Total: <$2/month

## Future Improvements

### Possible Enhancements (Not Planned)

1. **Token caching**: Cache tokens per repo+scopes to reduce GitHub API calls
  - Tradeoff: Added complexity, Redis/Memorystore cost

2. **Metrics and monitoring**: Export metrics for observability
  - Tradeoff: More code, potential log costs

3. **Request deduplication**: Reuse tokens for concurrent requests
  - Tradeoff: Distributed locking complexity

4. **Support for organization permissions**: Expand beyond repository permissions
  - Tradeoff: Significantly more complex permission model

5. **Custom token expiration**: Let caller specify expiry up to 1 hour
  - Tradeoff: More validation logic, potential security risk

**Philosophy**: Keep it simple. Only add features if they're clearly needed.
