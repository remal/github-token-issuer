package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v81/github"
)

// generateTestRSAKey creates a 2048-bit RSA key pair for testing JWT signing.
func generateTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	return key
}

// TestCreateJWT tests JWT creation for GitHub App authentication.
// It verifies that valid keys produce valid JWTs and nil keys return errors.
//
// Test steps:
//  1. Generate a test RSA key (or use nil for error cases)
//  2. Call CreateJWT with the key and app ID
//  3. Verify JWT is returned for valid keys (non-empty, 3-part structure)
//  4. Verify error is returned for nil keys
func TestCreateJWT(t *testing.T) {
	tests := []struct {
		name        string
		appID       string
		useNilKey   bool
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid private key and app ID",
			appID:     "12345",
			useNilKey: false,
			wantErr:   false,
		},
		{
			name:      "valid private key with different app ID",
			appID:     "987654321",
			useNilKey: false,
			wantErr:   false,
		},
		{
			name:      "valid private key with string app ID",
			appID:     "my-app-id",
			useNilKey: false,
			wantErr:   false,
		},
		{
			name:        "nil private key returns error",
			appID:       "12345",
			useNilKey:   true,
			wantErr:     true,
			errContains: "private key is nil",
		},
	}

	// Step 1: Generate a shared test key for valid cases
	validKey := generateTestRSAKey(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Determine which key to use
			var key *rsa.PrivateKey
			if !tt.useNilKey {
				key = validKey
			}

			// Step 2: Call CreateJWT
			got, err := CreateJWT(key, tt.appID)

			// Step 3 & 4: Verify results
			if tt.wantErr {
				// Verify error is returned
				if err == nil {
					t.Errorf("CreateJWT() error = nil, wantErr = true")
					return
				}
				// Verify error message contains expected text
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("CreateJWT() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			// Verify no error for valid keys
			if err != nil {
				t.Errorf("CreateJWT() unexpected error = %v", err)
				return
			}

			// Verify JWT is not empty
			if got == "" {
				t.Error("CreateJWT() returned empty string")
				return
			}

			// Verify JWT structure (3 parts separated by dots)
			parts := strings.Split(got, ".")
			if len(parts) != 3 {
				t.Errorf("CreateJWT() returned invalid JWT format, got %d parts, want 3", len(parts))
			}
		})
	}
}

// TestCreateJWT_Claims tests that JWT claims are correctly set.
// It verifies iat (issued at), exp (expiration), and iss (issuer) claims.
//
// Test steps:
//  1. Generate a test RSA key
//  2. Record timestamp before/after JWT creation
//  3. Call CreateJWT to generate a token
//  4. Parse the JWT and extract claims
//  5. Verify iss claim matches app ID
//  6. Verify iat claim is within expected time range
//  7. Verify exp claim is exactly 10 minutes after iat
func TestCreateJWT_Claims(t *testing.T) {
	// Step 1: Generate test key
	key := generateTestRSAKey(t)
	appID := "12345"

	// Step 2: Record timestamps
	beforeCreate := time.Now().Unix()
	// Step 3: Create JWT
	tokenString, err := CreateJWT(key, appID)
	afterCreate := time.Now().Unix()

	if err != nil {
		t.Fatalf("CreateJWT() error = %v", err)
	}

	// Step 4: Parse the token to verify claims
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return &key.PublicKey, nil
	})

	if err != nil {
		t.Fatalf("failed to parse JWT: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("failed to get claims from token")
	}

	// Step 5: Verify issuer claim
	if iss, ok := claims["iss"].(string); !ok || iss != appID {
		t.Errorf("JWT iss claim = %v, want %v", claims["iss"], appID)
	}

	// Step 6: Verify iat claim (issued at)
	if iat, ok := claims["iat"].(float64); !ok {
		t.Error("JWT iat claim missing or invalid type")
	} else {
		iatInt := int64(iat)
		if iatInt < beforeCreate || iatInt > afterCreate {
			t.Errorf("JWT iat claim = %v, want between %v and %v", iatInt, beforeCreate, afterCreate)
		}
	}

	// Step 7: Verify exp claim (expiration - should be ~10 minutes from iat)
	if exp, ok := claims["exp"].(float64); !ok {
		t.Error("JWT exp claim missing or invalid type")
	} else {
		iat := int64(claims["iat"].(float64))
		expInt := int64(exp)
		expectedExp := iat + 600 // 10 minutes in seconds

		if expInt != expectedExp {
			t.Errorf("JWT exp claim = %v, want %v (iat + 600 seconds)", expInt, expectedExp)
		}
	}
}

// TestCreateJWT_Algorithm tests that the JWT uses RS256 signing algorithm.
// GitHub requires RS256 for App authentication.
//
// Test steps:
//  1. Generate a test RSA key
//  2. Call CreateJWT to generate a token
//  3. Parse the JWT header without verification
//  4. Verify the algorithm is RS256
func TestCreateJWT_Algorithm(t *testing.T) {
	// Step 1: Generate test key
	key := generateTestRSAKey(t)
	appID := "12345"

	// Step 2: Create JWT
	tokenString, err := CreateJWT(key, appID)
	if err != nil {
		t.Fatalf("CreateJWT() error = %v", err)
	}

	// Step 3: Parse without verification to check header
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("failed to parse JWT: %v", err)
	}

	// Step 4: Verify RS256 algorithm
	if token.Method.Alg() != "RS256" {
		t.Errorf("JWT algorithm = %v, want RS256", token.Method.Alg())
	}
}

// TestVerifyRequestedScopes tests verification of requested vs granted scopes.
// It ensures the function correctly identifies missing or mismatched permissions.
//
// Test steps:
//  1. Create a map of requested scopes with permission levels
//  2. Create a mock InstallationPermissions struct with granted scopes
//  3. Call VerifyRequestedScopes with requested and granted
//  4. Verify no error when all scopes match
//  5. Verify error when scopes are missing or have wrong permission level
func TestVerifyRequestedScopes(t *testing.T) {
	tests := []struct {
		name        string
		requested   map[string]string
		granted     *github.InstallationPermissions
		wantErr     bool
		errContains string
	}{
		{
			name:      "single scope granted exactly",
			requested: map[string]string{"contents": "write"},
			granted:   &github.InstallationPermissions{Contents: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "single scope read granted",
			requested: map[string]string{"contents": "read"},
			granted:   &github.InstallationPermissions{Contents: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "multiple scopes all granted",
			requested: map[string]string{"contents": "write", "issues": "read", "pull_requests": "write"},
			granted: &github.InstallationPermissions{
				Contents:     github.Ptr("write"),
				Issues:       github.Ptr("read"),
				PullRequests: github.Ptr("write"),
			},
			wantErr: false,
		},
		{
			name:        "missing scope - contents requested but not granted",
			requested:   map[string]string{"contents": "write"},
			granted:     &github.InstallationPermissions{},
			wantErr:     true,
			errContains: "missing",
		},
		{
			name:        "wrong permission level - requested write, granted read",
			requested:   map[string]string{"contents": "write"},
			granted:     &github.InstallationPermissions{Contents: github.Ptr("read")},
			wantErr:     true,
			errContains: "missing",
		},
		{
			name:        "nil granted permissions",
			requested:   map[string]string{"contents": "read"},
			granted:     nil,
			wantErr:     true,
			errContains: "no permissions",
		},
		{
			name:      "extra granted scopes are ok",
			requested: map[string]string{"contents": "read"},
			granted: &github.InstallationPermissions{
				Contents: github.Ptr("read"),
				Issues:   github.Ptr("write"),
				Actions:  github.Ptr("read"),
			},
			wantErr: false,
		},
		{
			name:      "empty requested scopes",
			requested: map[string]string{},
			granted:   &github.InstallationPermissions{Contents: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "actions scope",
			requested: map[string]string{"actions": "write"},
			granted:   &github.InstallationPermissions{Actions: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "administration scope",
			requested: map[string]string{"administration": "read"},
			granted:   &github.InstallationPermissions{Administration: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "attestations scope",
			requested: map[string]string{"attestations": "write"},
			granted:   &github.InstallationPermissions{Attestations: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "checks scope",
			requested: map[string]string{"checks": "write"},
			granted:   &github.InstallationPermissions{Checks: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "dependabot_secrets scope",
			requested: map[string]string{"dependabot_secrets": "read"},
			granted:   &github.InstallationPermissions{DependabotSecrets: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "deployments scope",
			requested: map[string]string{"deployments": "write"},
			granted:   &github.InstallationPermissions{Deployments: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "discussions scope",
			requested: map[string]string{"discussions": "read"},
			granted:   &github.InstallationPermissions{Discussions: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "environments scope",
			requested: map[string]string{"environments": "write"},
			granted:   &github.InstallationPermissions{Environments: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "issues scope",
			requested: map[string]string{"issues": "write"},
			granted:   &github.InstallationPermissions{Issues: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "merge_queues scope",
			requested: map[string]string{"merge_queues": "read"},
			granted:   &github.InstallationPermissions{MergeQueues: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "packages scope",
			requested: map[string]string{"packages": "write"},
			granted:   &github.InstallationPermissions{Packages: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "pages scope",
			requested: map[string]string{"pages": "read"},
			granted:   &github.InstallationPermissions{Pages: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "projects scope maps to RepositoryProjects",
			requested: map[string]string{"projects": "write"},
			granted:   &github.InstallationPermissions{RepositoryProjects: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "pull_requests scope",
			requested: map[string]string{"pull_requests": "write"},
			granted:   &github.InstallationPermissions{PullRequests: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "secret_scanning scope maps to SecretScanningAlerts",
			requested: map[string]string{"secret_scanning": "read"},
			granted:   &github.InstallationPermissions{SecretScanningAlerts: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "secrets scope",
			requested: map[string]string{"secrets": "write"},
			granted:   &github.InstallationPermissions{Secrets: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:      "statuses scope",
			requested: map[string]string{"statuses": "read"},
			granted:   &github.InstallationPermissions{Statuses: github.Ptr("read")},
			wantErr:   false,
		},
		{
			name:      "workflows scope",
			requested: map[string]string{"workflows": "write"},
			granted:   &github.InstallationPermissions{Workflows: github.Ptr("write")},
			wantErr:   false,
		},
		{
			name:        "multiple missing scopes",
			requested:   map[string]string{"contents": "write", "issues": "write", "actions": "read"},
			granted:     &github.InstallationPermissions{Contents: github.Ptr("write")},
			wantErr:     true,
			errContains: "missing",
		},
		{
			name: "all scopes granted",
			requested: map[string]string{
				"actions":            "write",
				"administration":     "read",
				"attestations":       "write",
				"checks":             "write",
				"contents":           "write",
				"dependabot_secrets": "write",
				"deployments":        "write",
				"discussions":        "write",
				"environments":       "write",
				"issues":             "write",
				"merge_queues":       "write",
				"packages":           "write",
				"pages":              "write",
				"projects":           "write",
				"pull_requests":      "write",
				"secret_scanning":    "read",
				"secrets":            "write",
				"statuses":           "write",
				"workflows":          "write",
			},
			granted: &github.InstallationPermissions{
				Actions:              github.Ptr("write"),
				Administration:       github.Ptr("read"),
				Attestations:         github.Ptr("write"),
				Checks:               github.Ptr("write"),
				Contents:             github.Ptr("write"),
				DependabotSecrets:    github.Ptr("write"),
				Deployments:          github.Ptr("write"),
				Discussions:          github.Ptr("write"),
				Environments:         github.Ptr("write"),
				Issues:               github.Ptr("write"),
				MergeQueues:          github.Ptr("write"),
				Packages:             github.Ptr("write"),
				Pages:                github.Ptr("write"),
				RepositoryProjects:   github.Ptr("write"),
				PullRequests:         github.Ptr("write"),
				SecretScanningAlerts: github.Ptr("read"),
				Secrets:              github.Ptr("write"),
				Statuses:             github.Ptr("write"),
				Workflows:            github.Ptr("write"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 3: Call VerifyRequestedScopes
			err := VerifyRequestedScopes(tt.requested, tt.granted)

			// Step 4 & 5: Verify results
			if tt.wantErr {
				// Verify error is returned
				if err == nil {
					t.Errorf("VerifyRequestedScopes() error = nil, wantErr = true")
					return
				}
				// Verify error message contains expected text
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("VerifyRequestedScopes() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			// Verify no error when scopes match
			if err != nil {
				t.Errorf("VerifyRequestedScopes() unexpected error = %v", err)
			}
		})
	}
}

// TestNewGitHubClientWithJWT tests GitHub client creation with JWT authentication.
// It verifies the function returns a non-nil client.
//
// Test steps:
//  1. Call NewGitHubClientWithJWT with a test token
//  2. Verify the returned client is not nil
func TestNewGitHubClientWithJWT(t *testing.T) {
	// Step 1: Create client with test token
	token := "test-jwt-token"
	client := NewGitHubClientWithJWT(token)

	// Step 2: Verify client is not nil
	if client == nil {
		t.Error("NewGitHubClientWithJWT() returned nil")
	}
}

// mockAppsService implements GitHubAppsService for testing.
type mockAppsService struct {
	findRepoInstallation    func(ctx context.Context, owner, repo string) (*github.Installation, *github.Response, error)
	createInstallationToken func(ctx context.Context, id int64, opts *github.InstallationTokenOptions) (*github.InstallationToken, *github.Response, error)
}

func (m *mockAppsService) FindRepositoryInstallation(ctx context.Context, owner, repo string) (*github.Installation, *github.Response, error) {
	return m.findRepoInstallation(ctx, owner, repo)
}

func (m *mockAppsService) CreateInstallationToken(ctx context.Context, id int64, opts *github.InstallationTokenOptions) (*github.InstallationToken, *github.Response, error) {
	return m.createInstallationToken(ctx, id, opts)
}

// TestGetInstallationID tests finding the GitHub App installation ID for a repository.
// It verifies correct handling of valid repositories, invalid formats, and API errors.
//
// Test steps:
//  1. Create mock GitHubAppsService with configured response
//  2. Call GetInstallationID with test repository
//  3. Verify returned installation ID matches expected value
//  4. Verify error handling for various failure scenarios
func TestGetInstallationID(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		repository   string
		mockResponse *github.Installation
		mockResp     *github.Response
		mockErr      error
		wantID       int64
		wantErr      bool
		errContains  string
	}{
		{
			name:         "valid repository returns installation ID",
			repository:   "owner/repo",
			mockResponse: &github.Installation{ID: github.Ptr(int64(12345))},
			mockResp:     &github.Response{Response: &http.Response{StatusCode: http.StatusOK}},
			mockErr:      nil,
			wantID:       12345,
			wantErr:      false,
		},
		{
			name:         "different installation ID",
			repository:   "org/project",
			mockResponse: &github.Installation{ID: github.Ptr(int64(99999))},
			mockResp:     &github.Response{Response: &http.Response{StatusCode: http.StatusOK}},
			mockErr:      nil,
			wantID:       99999,
			wantErr:      false,
		},
		{
			name:        "invalid repository format - no slash",
			repository:  "invalidrepo",
			wantErr:     true,
			errContains: "invalid repository format",
		},
		{
			name:        "invalid repository format - empty",
			repository:  "",
			wantErr:     true,
			errContains: "invalid repository format",
		},
		{
			name:        "invalid repository format - too many parts",
			repository:  "a/b/c",
			wantErr:     true,
			errContains: "invalid repository format",
		},
		{
			name:         "app not installed - 404 response",
			repository:   "owner/repo",
			mockResponse: nil,
			mockResp:     &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}},
			mockErr:      fmt.Errorf("not found"),
			wantErr:      true,
			errContains:  "not installed",
		},
		{
			name:         "API error",
			repository:   "owner/repo",
			mockResponse: nil,
			mockResp:     &github.Response{Response: &http.Response{StatusCode: http.StatusInternalServerError}},
			mockErr:      fmt.Errorf("internal server error"),
			wantErr:      true,
			errContains:  "failed to find installation",
		},
		{
			name:         "installation ID is nil",
			repository:   "owner/repo",
			mockResponse: &github.Installation{ID: nil},
			mockResp:     &github.Response{Response: &http.Response{StatusCode: http.StatusOK}},
			mockErr:      nil,
			wantErr:      true,
			errContains:  "installation ID is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create mock service
			mock := &mockAppsService{
				findRepoInstallation: func(ctx context.Context, owner, repo string) (*github.Installation, *github.Response, error) {
					return tt.mockResponse, tt.mockResp, tt.mockErr
				},
			}

			// Step 2: Call GetInstallationID
			gotID, err := GetInstallationID(ctx, mock, tt.repository)

			// Step 3 & 4: Verify results
			if tt.wantErr {
				if err == nil {
					t.Errorf("GetInstallationID() error = nil, wantErr = true")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("GetInstallationID() error = %v, want containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("GetInstallationID() unexpected error = %v", err)
				return
			}

			if gotID != tt.wantID {
				t.Errorf("GetInstallationID() = %v, want %v", gotID, tt.wantID)
			}
		})
	}
}

// TestCreateInstallationToken tests requesting an installation access token from GitHub.
// It verifies correct permission mapping, error handling, and scope verification.
//
// Test steps:
//  1. Create mock GitHubAppsService with configured response
//  2. Call CreateInstallationToken with test scopes
//  3. Verify returned token matches expected value
//  4. Verify error handling for various failure scenarios
func TestCreateInstallationToken(t *testing.T) {
	ctx := context.Background()
	testTime := time.Now().Add(1 * time.Hour)

	tests := []struct {
		name        string
		installID   int64
		scopes      map[string]string
		mockToken   *github.InstallationToken
		mockResp    *github.Response
		mockErr     error
		wantErr     bool
		errContains string
	}{
		{
			name:      "single scope - contents write",
			installID: 12345,
			scopes:    map[string]string{"contents": "write"},
			mockToken: &github.InstallationToken{
				Token:       github.Ptr("ghs_test123"),
				ExpiresAt:   &github.Timestamp{Time: testTime},
				Permissions: &github.InstallationPermissions{Contents: github.Ptr("write")},
			},
			mockResp: &github.Response{Response: &http.Response{StatusCode: http.StatusCreated}},
			mockErr:  nil,
			wantErr:  false,
		},
		{
			name:      "multiple scopes",
			installID: 12345,
			scopes:    map[string]string{"contents": "read", "issues": "write", "pull_requests": "read"},
			mockToken: &github.InstallationToken{
				Token:     github.Ptr("ghs_multi"),
				ExpiresAt: &github.Timestamp{Time: testTime},
				Permissions: &github.InstallationPermissions{
					Contents:     github.Ptr("read"),
					Issues:       github.Ptr("write"),
					PullRequests: github.Ptr("read"),
				},
			},
			mockResp: &github.Response{Response: &http.Response{StatusCode: http.StatusCreated}},
			mockErr:  nil,
			wantErr:  false,
		},
		{
			name:        "forbidden - insufficient permissions",
			installID:   12345,
			scopes:      map[string]string{"contents": "write"},
			mockToken:   nil,
			mockResp:    &github.Response{Response: &http.Response{StatusCode: http.StatusForbidden}},
			mockErr:     fmt.Errorf("forbidden"),
			wantErr:     true,
			errContains: "insufficient permissions",
		},
		{
			name:        "unprocessable entity - suspended installation",
			installID:   12345,
			scopes:      map[string]string{"contents": "write"},
			mockToken:   nil,
			mockResp:    &github.Response{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}},
			mockErr:     fmt.Errorf("unprocessable"),
			wantErr:     true,
			errContains: "suspended",
		},
		{
			name:        "API error",
			installID:   12345,
			scopes:      map[string]string{"contents": "write"},
			mockToken:   nil,
			mockResp:    &github.Response{Response: &http.Response{StatusCode: http.StatusInternalServerError}},
			mockErr:     fmt.Errorf("internal error"),
			wantErr:     true,
			errContains: "failed to create installation token",
		},
		{
			name:        "unknown scope ID",
			installID:   12345,
			scopes:      map[string]string{"unknown_scope": "read"},
			wantErr:     true,
			errContains: "unknown scope ID",
		},
		{
			name:      "GitHub returns fewer scopes than requested",
			installID: 12345,
			scopes:    map[string]string{"contents": "write", "issues": "write"},
			mockToken: &github.InstallationToken{
				Token:       github.Ptr("ghs_partial"),
				ExpiresAt:   &github.Timestamp{Time: testTime},
				Permissions: &github.InstallationPermissions{Contents: github.Ptr("write")},
			},
			mockResp:    &github.Response{Response: &http.Response{StatusCode: http.StatusCreated}},
			mockErr:     nil,
			wantErr:     true,
			errContains: "fewer scopes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create mock service
			mock := &mockAppsService{
				createInstallationToken: func(ctx context.Context, id int64, opts *github.InstallationTokenOptions) (*github.InstallationToken, *github.Response, error) {
					return tt.mockToken, tt.mockResp, tt.mockErr
				},
			}

			// Step 2: Call CreateInstallationToken
			token, err := CreateInstallationToken(ctx, mock, tt.installID, tt.scopes)

			// Step 3 & 4: Verify results
			if tt.wantErr {
				if err == nil {
					t.Errorf("CreateInstallationToken() error = nil, wantErr = true")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("CreateInstallationToken() error = %v, want containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("CreateInstallationToken() unexpected error = %v", err)
				return
			}

			if token == nil {
				t.Error("CreateInstallationToken() returned nil token")
				return
			}

			if token.GetToken() != tt.mockToken.GetToken() {
				t.Errorf("CreateInstallationToken() token = %v, want %v", token.GetToken(), tt.mockToken.GetToken())
			}
		})
	}
}
