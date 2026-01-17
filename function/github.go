package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v81/github"
)

// GetPrivateKey fetches the GitHub App private key from GCP Secret Manager.
func GetPrivateKey(ctx context.Context, projectID string) (privateKey *rsa.PrivateKey, err error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Secret Manager client: %w", err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close Secret Manager client: %w", closeErr)
		}
	}()

	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/github-app-private-key/versions/latest", projectID),
	}

	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve private key from Secret Manager: %w", err)
	}

	// Parse PEM-encoded private key
	block, _ := pem.Decode(result.Payload.Data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from private key")
	}

	// Try PKCS1 format first (RSA PRIVATE KEY)
	privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8 format (PRIVATE KEY)
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
		var ok bool
		privateKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is not an RSA private key")
		}
	}

	return privateKey, nil
}

// CreateJWT creates a JWT for authenticating as the GitHub App.
// JWT expires in 10 minutes (GitHub's maximum allowed).
func CreateJWT(privateKey *rsa.PrivateKey, appID string) (string, error) {
	if privateKey == nil {
		return "", fmt.Errorf("private key is nil")
	}

	now := time.Now()

	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedToken, err := token.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return signedToken, nil
}

// GitHubAppsService defines the GitHub Apps API methods used by this package.
type GitHubAppsService interface {
	FindRepositoryInstallation(ctx context.Context, owner, repo string) (*github.Installation, *github.Response, error)
	CreateInstallationToken(ctx context.Context, id int64, opts *github.InstallationTokenOptions) (*github.InstallationToken, *github.Response, error)
}

// GetInstallationID finds the GitHub App installation ID for the given repository.
func GetInstallationID(ctx context.Context, apps GitHubAppsService, repository string) (int64, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid repository format: %s", repository)
	}

	owner, repo := parts[0], parts[1]

	installation, resp, err := apps.FindRepositoryInstallation(ctx, owner, repo)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return 0, fmt.Errorf("GitHub App is not installed on repository %s", repository)
		}
		return 0, fmt.Errorf("failed to find installation: %w", err)
	}

	if installation.ID == nil {
		return 0, fmt.Errorf("installation ID is nil for repository %s", repository)
	}

	return *installation.ID, nil
}

// CreateInstallationToken requests an installation access token from GitHub with the specified permissions.
func CreateInstallationToken(ctx context.Context, apps GitHubAppsService, installationID int64, scopes map[string]string) (*github.InstallationToken, error) {
	// Build permissions map
	permissions := &github.InstallationPermissions{}

	// Use reflection-free approach: map scope IDs to struct fields
	for scopeID, permission := range scopes {
		permValue := github.Ptr(permission)

		switch scopeID {
		case "actions":
			permissions.Actions = permValue
		case "administration":
			permissions.Administration = permValue
		case "attestations":
			permissions.Attestations = permValue
		case "checks":
			permissions.Checks = permValue
		case "contents":
			permissions.Contents = permValue
		case "dependabot_secrets":
			permissions.DependabotSecrets = permValue
		case "deployments":
			permissions.Deployments = permValue
		case "discussions":
			permissions.Discussions = permValue
		case "environments":
			permissions.Environments = permValue
		case "issues":
			permissions.Issues = permValue
		case "merge_queues":
			permissions.MergeQueues = permValue
		case "packages":
			permissions.Packages = permValue
		case "pages":
			permissions.Pages = permValue
		case "projects":
			permissions.RepositoryProjects = permValue
		case "pull_requests":
			permissions.PullRequests = permValue
		case "secret_scanning":
			permissions.SecretScanningAlerts = permValue
		case "secrets":
			permissions.Secrets = permValue
		case "statuses":
			permissions.Statuses = permValue
		case "workflows":
			permissions.Workflows = permValue
		default:
			return nil, fmt.Errorf("unknown scope ID: %s", scopeID)
		}
	}

	opts := &github.InstallationTokenOptions{
		Permissions: permissions,
	}

	token, resp, err := apps.CreateInstallationToken(ctx, installationID, opts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("insufficient permissions for requested scopes")
		}
		if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
			return nil, fmt.Errorf("GitHub App installation is suspended or has insufficient permissions")
		}
		return nil, fmt.Errorf("failed to create installation token: %w", err)
	}

	// Verify that GitHub granted all requested scopes
	if err := VerifyRequestedScopes(scopes, token.GetPermissions()); err != nil {
		return nil, err
	}

	return token, nil
}

// VerifyRequestedScopes verifies that GitHub granted all requested scopes.
func VerifyRequestedScopes(requested map[string]string, granted *github.InstallationPermissions) error {
	if granted == nil {
		return fmt.Errorf("GitHub API returned no permissions")
	}

	grantedMap := make(map[string]string)

	// Convert granted permissions to map
	if granted.Actions != nil {
		grantedMap["actions"] = *granted.Actions
	}
	if granted.Administration != nil {
		grantedMap["administration"] = *granted.Administration
	}
	if granted.Attestations != nil {
		grantedMap["attestations"] = *granted.Attestations
	}
	if granted.Checks != nil {
		grantedMap["checks"] = *granted.Checks
	}
	if granted.Contents != nil {
		grantedMap["contents"] = *granted.Contents
	}
	if granted.DependabotSecrets != nil {
		grantedMap["dependabot_secrets"] = *granted.DependabotSecrets
	}
	if granted.Deployments != nil {
		grantedMap["deployments"] = *granted.Deployments
	}
	if granted.Discussions != nil {
		grantedMap["discussions"] = *granted.Discussions
	}
	if granted.Environments != nil {
		grantedMap["environments"] = *granted.Environments
	}
	if granted.Issues != nil {
		grantedMap["issues"] = *granted.Issues
	}
	if granted.MergeQueues != nil {
		grantedMap["merge_queues"] = *granted.MergeQueues
	}
	if granted.Packages != nil {
		grantedMap["packages"] = *granted.Packages
	}
	if granted.Pages != nil {
		grantedMap["pages"] = *granted.Pages
	}
	if granted.RepositoryProjects != nil {
		grantedMap["projects"] = *granted.RepositoryProjects
	}
	if granted.PullRequests != nil {
		grantedMap["pull_requests"] = *granted.PullRequests
	}
	if granted.SecretScanningAlerts != nil {
		grantedMap["secret_scanning"] = *granted.SecretScanningAlerts
	}
	if granted.Secrets != nil {
		grantedMap["secrets"] = *granted.Secrets
	}
	if granted.Statuses != nil {
		grantedMap["statuses"] = *granted.Statuses
	}
	if granted.Workflows != nil {
		grantedMap["workflows"] = *granted.Workflows
	}

	// Check if all requested scopes were granted
	var missing []string
	for scopeID, requestedPerm := range requested {
		grantedPerm, exists := grantedMap[scopeID]
		if !exists || grantedPerm != requestedPerm {
			missing = append(missing, scopeID)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("GitHub API returned fewer scopes than requested (missing: %v)", missing)
	}

	return nil
}

// NewGitHubClientWithJWT creates a GitHub client authenticated with a JWT.
func NewGitHubClientWithJWT(jwtToken string) *github.Client {
	return github.NewClient(nil).WithAuthToken(jwtToken)
}
