# GitHub Token Issuer App

A secure, serverless GitHub App hosted on Google Cloud Platform that issues short-lived, scoped GitHub installation tokens to GitHub Actions workflows.

## Table of Contents

- [Overview](#overview)
- [Usage](#usage)
- [Architecture](#architecture)
- [Technical Specifications](#technical-specifications)
- [API Design](#api-design)
- [Authentication & Security](#authentication--security)
- [Scope Management](#scope-management)
- [Token Management](#token-management)
- [Error Handling](#error-handling)
- [Infrastructure](#infrastructure)
- [Deployment](#deployment)
- [Repository Structure](#repository-structure)

## Overview

This GitHub App provides a secure mechanism for GitHub Actions workflows to obtain short-lived GitHub installation tokens with specific scopes and permissions. The app runs as a Cloud Run Function on GCP and authenticates callers using GitHub's OIDC tokens integrated with GCP IAM.

### Why Use This?

GitHub Actions workflows typically use the built-in `GITHUB_TOKEN`, but it has significant limitations:

- **Cannot trigger other workflows**: Actions performed with `GITHUB_TOKEN` (especially with `contents: write`) do not trigger subsequent GitHub Actions workflows, breaking automation chains
- **Limited scope control**: You cannot request tokens with only the specific scopes you need
- **Repository-bound**: The default token is tied to the repository running the workflow
- **Requires manual secret management**: Personal Access Tokens (PATs) must be created, stored in GitHub Secrets, rotated manually, and managed across all repositories

This app solves these problems by issuing short-lived GitHub App installation tokens that:

- **Trigger workflows**: Operations performed with these tokens trigger GitHub Actions normally
- **Fine-grained repository permissions**: Request only the specific repository-level scopes you need (e.g., `issues:write`, `pull_requests:read`, `deployments:write`)
- **Enhanced security**: Short-lived tokens (1 hour expiration) minimize exposure risk
- **No secret management required**: Just install the GitHub App on your repositories and use the action - no need to create, store, or rotate tokens in GitHub Secrets
- **Centralized access control**: Install the app once, use it across all repositories without duplicating secrets
- **Easier onboarding**: New repositories can start using tokens immediately after app installation, no manual secret configuration needed

**Note**: This app only issues tokens with **repository-level permissions**. Organization-level or account-level permissions are not supported.

### Key Features

- Serverless Cloud Run Function for automatic scaling
- GitHub OIDC token validation via GCP IAM
- Scope allowlisting and blacklisting for security
- Simple API with query parameter-based scope specification
- Automated CI/CD pipeline using GitHub Actions and Terraform
- Minimal operational overhead with no logging or monitoring complexity

## Usage

### Composite GitHub Action

The repository includes a composite action (`action.yml`) that simplifies calling the function from workflows.

**Location**: `./action.yml` in repository root

**Inputs**:

- `scopes`: (required) Repository permission scopes in format `scope_id:permission`, one per line
  - Use scope IDs from the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) table
  - Example:
    ```yaml
    scopes: |
      issues:write
      pull_requests:read
      deployments:write
    ```

**Outputs**:

- `token`: The issued GitHub installation token

**Example Usage**:

```yaml
name: Deploy

on:
  push:
    branches: [ main ]

permissions:
  id-token: write  # Required for OIDC token

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
    - name: Get GitHub Token
      id: get-token
      uses: your-org/github-token-issuer@main
      with:
        scopes: |
          contents:write
          deployments:write
          statuses:write

    - name: Use Token
      env:
        GITHUB_TOKEN: ${{ steps.get-token.outputs.token }}
      run: |
        # Use the token for authenticated GitHub API calls that trigger workflows
        # Unlike the default GITHUB_TOKEN, this will trigger subsequent workflow runs
        git config user.name "github-actions[bot]"
        git config user.email "github-actions[bot]@users.noreply.github.com"
        echo "deployed" > deployment.txt
        git add deployment.txt
        git commit -m "Deploy to production"
        git push
```

### Manual API Call (for testing)

```bash
# Obtain OIDC token from GitHub Actions
OIDC_TOKEN=$(curl -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
  "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https://github-token-issuer-xyz.run.app" | jq -r .value)

# Call the function with repository permission scopes
curl -X POST \
  -H "Authorization: Bearer ${OIDC_TOKEN}" \
  "https://github-token-issuer-xyz.run.app/token?contents=write&deployments=write&statuses=write"
```

### Allowed Repository Permission Scopes

**Important**: This app only works with **repository-level permissions**. Organization-level and account-level permissions are not supported.

The following repository permission scopes are allowed (use the Scope ID in your action):

| Permission Name                | Scope ID              | Available Levels |
|--------------------------------|-----------------------|------------------|
| Actions                        | `actions`             | read, write      |
| Administration                 | `administration`      | read             |
| Attestations                   | `attestations`        | read, write      |
| Checks                         | `checks`              | read, write      |
| Code scanning alerts           | `code_scanning`       | read             |
| Commit statuses                | `statuses`            | read, write      |
| Contents                       | `contents`            | read, write      |
| Custom properties              | `custom_properties`   | read, write      |
| Dependabot alerts              | `dependabot_alerts`   | read             |
| Dependabot secrets             | `dependabot_secrets`  | read, write      |
| Deployments                    | `deployments`         | read, write      |
| Discussions                    | `discussions`         | read, write      |
| Environments                   | `environments`        | read, write      |
| Issues                         | `issues`              | read, write      |
| Merge queues                   | `merge_queues`        | read, write      |
| Packages                       | `packages`            | read, write      |
| Pages                          | `pages`               | read, write      |
| Projects                       | `projects`            | read, write      |
| Pull requests                  | `pull_requests`       | read, write      |
| Repository security advisories | `security_advisories` | read             |
| Secret scanning alerts         | `secret_scanning`     | read             |
| Secrets                        | `secrets`             | read, write      |
| Variables                      | `variables`           | read, write      |
| Workflows                      | `workflows`           | read, write      |

**Note**: Some security-related scopes are restricted to read-only access in this app for safety reasons:

- `code_scanning` - Code scanning alerts
- `dependabot_alerts` - Dependabot alerts
- `security_advisories` - Repository security advisories
- `secret_scanning` - Secret scanning alerts

### Error Code Catalog

| Error Message                                        | Cause                                                         | Resolution                                                                                                                              |
|------------------------------------------------------|---------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------|
| `duplicate scope 'X' in request`                     | Same scope appears multiple times in query params             | Remove duplicate scopes - each scope should appear only once                                                                            |
| `scope 'X' is not allowed`                           | Requested scope is blacklisted or not a repository permission | Check the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) table for valid repository permission scope IDs |
| `scope 'X' is not in allowlist`                      | Requested scope ID is not recognized                          | Use a valid scope ID from the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) table                       |
| `GitHub App is not installed on repository`          | App not installed on the target repository                    | Install the GitHub App on the repository in GitHub settings                                                                             |
| `insufficient permissions for scope 'X'`             | App doesn't have repository permission for requested scope    | Update GitHub App's repository permissions or request fewer scopes                                                                      |
| `GitHub API returned fewer scopes than requested`    | Repository-level restrictions limit available scopes          | Check repository settings and branch protection rules                                                                                   |
| `GitHub App installation is suspended`               | App has been suspended                                        | Check GitHub App status and resolve suspension                                                                                          |
| `failed to retrieve private key from Secret Manager` | Secret Manager unavailable or misconfigured                   | Verify Secret Manager permissions and secret exists                                                                                     |

## Architecture

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

### Request Flow

1. **GitHub Actions Workflow** generates OIDC token and calls Cloud Run Function
2. **GCP IAM** validates GitHub OIDC token for Cloud Run invocation
3. **Function** extracts repository from OIDC claims
4. **Function** parses scope permissions from query parameters
5. **Function** validates scopes against hardcoded allowlist/blacklist
6. **Function** fetches GitHub App private key from Secret Manager
7. **Function** creates JWT (10-minute expiry) to authenticate as GitHub App
8. **Function** queries GitHub API for App installation and permissions
9. **Function** creates installation token (1-hour expiry) with requested scopes
10. **Function** returns token and metadata as JSON response

## Technical Specifications

### Runtime Environment

- **Language**: Go 1.23+ (latest stable)
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
  - Build stage: golang:1.23 with full SDK
  - Runtime stage: distroless or scratch for minimal image size
- **CI/CD**: GitHub Actions workflow (.github/workflows/deploy.yml)
  - Triggered on push to main branch
  - Steps: Lint → Terraform plan review → Deploy

## API Design

### Endpoint Structure

**Single Endpoint**:

```
POST https://github-token-issuer-[hash]-[region].a.run.app/token
```

### Query Parameters (Scope Specification)

Scopes are specified as query parameters where the parameter name is the **repository permission scope ID** (e.g., `contents`, `issues`, `pull_requests`) and the value is the permission level (`read` or `write`).

See the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) section in Usage for the complete list of scope IDs.

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

**Duplicate Handling**: If the same scope appears multiple times (even with the same permission), the function returns a **400 Bad Request** error. This helps catch incorrect action usage.

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

## Authentication & Security

### Authentication Flow

**GitHub OIDC Token as IAM Bearer Token**:

- The GitHub Actions OIDC token is used directly as the Cloud Run IAM bearer token
- GCP IAM validates the token signature and claims
- No additional authentication layer required

### Required OIDC Claims

The function extracts the following claim from the OIDC token:

- **`repository`**: Used to identify which repository the token should be issued for

### Private Key Security

- GitHub App private key stored in GCP Secret Manager
- Fetched on every request (no caching) to ensure freshness
- If Secret Manager is unavailable, request fails immediately (no fallback)

### Key Rotation Strategy

**Blue-Green Deployment Approach**:

1. Generate new GitHub App private key
2. Update Secret Manager with new key
3. Deploy new function version with reference to new secret
4. Switch Cloud Run traffic to new version
5. Revoke old private key on GitHub

## Scope Management

### Scope Storage and Configuration

**Storage**: Allowed and blacklisted scopes are hardcoded in `function/scopes.go` as constants/maps. See the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) section in Usage for the complete list of supported scopes.

### Blacklist

**Forbidden scopes** (high-privilege operations that are explicitly blocked):

Currently, all repository permissions listed in the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) section at their specified levels are allowed. The blacklist can be customized in `function/scopes.go` to block specific scopes if needed for your security requirements.

### Validation Logic

1. Parse all scope query parameters (repository permission scope IDs)
2. Check for duplicate scopes → **Reject with 400 if any scope appears more than once**
3. Check if any requested scope is in the blacklist → **Reject entire request (400)**
4. Check if all requested scopes are in the allowlist → **Reject if any scope not allowed (400)**
5. Extract repository from OIDC token and query GitHub API for App's granted repository permissions on that installation
6. Verify each requested scope+permission doesn't exceed App's granted repository permissions
7. Request installation token from GitHub API with exact scopes
8. If GitHub returns fewer scopes than requested → **Fail with error (403)**

### Scope Validation Rules

When parsing query parameters:

- Each scope must be a valid repository permission scope ID from the [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) list
- Each scope can have either `read` or `write` permission (as specified in the allowed levels)
- Each scope must appear only once; duplicate scopes result in **400 Bad Request**
- Only repository-level permissions are supported; organization or account permissions are not allowed
- This strict validation helps catch misconfigured actions early

## Token Management

### Installation Token Properties

- **Expiration**: Fixed 1 hour (GitHub's maximum allowed duration)
- **Scope Matching**: Must receive exactly the scopes requested; partial grants are rejected
- **No Caching**: Each request creates a new token; no token reuse across requests

### JWT Authentication

The function authenticates as the GitHub App using JWT:

- **Algorithm**: RS256 (RSA signature with SHA-256)
- **Expiration**: 10 minutes (GitHub's maximum)
- **Library**: go-github's built-in JWT methods
- **Claims**:
  - `iat`: Issued at timestamp
  - `exp`: Expiration timestamp (iat + 10 minutes)
  - `iss`: GitHub App ID

### Concurrent Request Handling

**No Coordination Strategy**:

- Each Cloud Run instance handles requests independently
- No token caching or request deduplication
- Each request fetches fresh data from Secret Manager and GitHub API
- Simplicity over optimization; acceptable for low-volume workloads

## Error Handling

### HTTP Status Codes

| Status Code                   | Scenario                                               | Example                                                               |
|-------------------------------|--------------------------------------------------------|-----------------------------------------------------------------------|
| **200 OK**                    | Success                                                | Token issued with requested scopes                                    |
| **400 Bad Request**           | Duplicate scopes, blacklisted scope, or invalid format | `{"error": "duplicate scope 'issues' in request"}`                    |
| **401 Unauthorized**          | Invalid OIDC token                                     | `{"error": "invalid OIDC token"}`                                     |
| **403 Forbidden**             | App not installed on repo or insufficient permissions  | `{"error": "GitHub App is not installed on repository myorg/myrepo"}` |
| **503 Service Unavailable**   | GitHub API degraded/unavailable                        | `{"error": "GitHub API is temporarily unavailable"}`                  |
| **500 Internal Server Error** | Secret Manager failure, internal errors                | `{"error": "failed to retrieve private key from Secret Manager"}`     |

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

### Failure Handling

- **GitHub API Outage**: Fail fast with 503, rely on caller to retry
- **Secret Manager Unavailable**: Fail immediately (no caching or fallback)
- **Archived Repository**: Attempt token issuance anyway; let GitHub API return error if necessary
- **Suspended GitHub App Installation**: Return 403 with clear error message

### Logging Strategy

**No Logging**:

- No logs for authentication failures
- No debug logs for requests
- No metrics or observability infrastructure
- Keep implementation simple and cost-minimal

## Infrastructure

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
- Configure **Repository permissions** only (see [Allowed Repository Permission Scopes](#allowed-repository-permission-scopes) for the full list)
  - Example: Contents (read/write), Issues (read/write), Pull requests (read/write), Deployments (read/write)
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

### CI/CD Pipeline

GitHub Actions workflow (`.github/workflows/deploy.yml`) automates deployment:

**Triggers**: Push to `main` branch

**Steps**:

1. **Lint & Code Quality**: Run `golangci-lint` on `function/` directory
2. **Build**: Compile Go binary from `function/` directory
3. **Terraform Plan**: Generate and review infrastructure changes from `terraform/` directory
4. **Deploy**: Deploy new revision to Cloud Run

## Repository Structure

```
.
├── function/                  # Cloud Run Function source code
│   ├── main.go                # Function entrypoint
│   ├── handlers.go            # HTTP request handlers
│   ├── github.go              # GitHub API client and JWT logic
│   ├── validation.go          # Scope validation and OIDC claims
│   ├── scopes.go              # Hardcoded allowlist/blacklist
│   ├── go.mod                 # Go module definition
│   ├── go.sum                 # Go dependency checksums
│   └── Dockerfile             # Multi-stage build for Cloud Run
├── terraform/                 # Infrastructure as Code
│   ├── main.tf                # Terraform infrastructure definition
│   ├── variables.tf           # Terraform variables
│   └── outputs.tf             # Terraform outputs (Cloud Run URL)
├── action.yml                 # Composite GitHub Action
├── .github/
│   └── workflows/
│       └── deploy.yml         # CI/CD deployment workflow
└── README.md                  # This file

```

## Contributing

This is a single-purpose utility. Contributions should maintain simplicity and avoid feature creep.

## Support

For issues or questions, open a GitHub issue in this repository.
