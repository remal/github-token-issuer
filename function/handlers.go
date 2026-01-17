package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// TokenResponse is the successful response format.
type TokenResponse struct {
	Token     string            `json:"token"`
	ExpiresAt string            `json:"expires_at"`
	Scopes    map[string]string `json:"scopes"`
}

// ErrorResponse is the error response format.
type ErrorResponse struct {
	Error   string                 `json:"error"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// TokenHandler handles POST /token requests.
func TokenHandler(w http.ResponseWriter, r *http.Request) {
	// Create logger (only logs if invoked via tag URL)
	logger := NewRequestLogger(r)

	// Only allow POST method
	if r.Method != http.MethodPost {
		logger.LogValidationError("method", r.Method)
		logger.LogResponse(http.StatusMethodNotAllowed, nil)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Extract OIDC token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		logger.LogValidationError("auth", "missing header")
		logger.LogResponse(http.StatusUnauthorized, nil)
		writeError(w, http.StatusUnauthorized, "missing Authorization header", nil)
		return
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		logger.LogValidationError("auth", "invalid format")
		logger.LogResponse(http.StatusUnauthorized, nil)
		writeError(w, http.StatusUnauthorized, "invalid Authorization header format", nil)
		return
	}

	oidcToken := parts[1]

	// Extract repository from OIDC token
	repository, err := ExtractRepositoryFromOIDC(oidcToken)
	if err != nil {
		logger.LogValidationError("oidc", "invalid token")
		logger.LogResponse(http.StatusUnauthorized, nil)
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("invalid OIDC token: %v", err), nil)
		return
	}
	logger.SetRepository(repository)

	// Parse scopes from query parameters
	scopes := make(map[string]string)
	for param, values := range r.URL.Query() {
		if len(values) > 1 {
			logger.LogValidationError("scope", fmt.Sprintf("duplicate: %s", param))
			logger.LogResponse(http.StatusBadRequest, nil)
			writeError(w, http.StatusBadRequest, fmt.Sprintf("duplicate scope '%s' in request", param), nil)
			return
		}
		permission := values[0]

		// Validate permission value
		if permission != "read" && permission != "write" {
			logger.LogValidationError("scope", fmt.Sprintf("invalid permission: %s=%s", param, permission))
			logger.LogResponse(http.StatusBadRequest, nil)
			writeError(w,
				http.StatusBadRequest,
				fmt.Sprintf("invalid permission '%s' for scope '%s' (must be 'read' or 'write')", permission, param),
				nil)
			return
		}

		scopes[param] = permission
	}

	// Require at least one scope
	if len(scopes) == 0 {
		logger.LogValidationError("scope", "none provided")
		logger.LogResponse(http.StatusBadRequest, nil)
		writeError(w, http.StatusBadRequest, "at least one scope is required", nil)
		return
	}

	// Log incoming request
	logger.LogRequest(scopes)

	// Validate scopes
	if err := ValidateScopes(scopes); err != nil {
		logger.LogValidationError("scope", err.Error())
		logger.LogResponse(http.StatusBadRequest, nil)
		writeError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}

	// Get GitHub App ID from environment
	appID := os.Getenv("GITHUB_APP_ID")
	if appID == "" {
		logger.LogValidationError("config", "GITHUB_APP_ID not set")
		logger.LogResponse(http.StatusInternalServerError, nil)
		writeError(w, http.StatusInternalServerError, "GITHUB_APP_ID not configured", nil)
		return
	}

	// Get GCP project ID from environment
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		// Try alternative environment variable
		projectID = os.Getenv("GCP_PROJECT")
	}
	if projectID == "" {
		logger.LogValidationError("config", "GCP project ID not set")
		logger.LogResponse(http.StatusInternalServerError, nil)
		writeError(w, http.StatusInternalServerError, "GCP project ID not configured", nil)
		return
	}

	// Fetch private key from Secret Manager
	privateKey, err := GetPrivateKey(ctx, projectID)
	if err != nil {
		logger.LogGitHubAPICall("get_private_key", false, err.Error())
		logger.LogResponse(http.StatusInternalServerError, nil)
		writeError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	logger.LogGitHubAPICall("get_private_key", true, "")

	// Create JWT for GitHub App authentication
	jwtToken, err := CreateJWT(privateKey, appID)
	if err != nil {
		logger.LogGitHubAPICall("create_jwt", false, err.Error())
		logger.LogResponse(http.StatusInternalServerError, nil)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create JWT: %v", err), nil)
		return
	}
	logger.LogGitHubAPICall("create_jwt", true, "")

	// Create GitHub client with JWT
	githubClient := NewGitHubClientWithJWT(jwtToken)

	// Get installation ID for repository
	installationID, err := GetInstallationID(ctx, githubClient.Apps, repository)
	if err != nil {
		logger.LogGitHubAPICall("get_installation_id", false, err.Error())
		if strings.Contains(err.Error(), "not installed") {
			logger.LogResponse(http.StatusForbidden, nil)
			writeError(w, http.StatusForbidden, err.Error(), nil)
		} else {
			logger.LogResponse(http.StatusServiceUnavailable, nil)
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("GitHub API error: %v", err), nil)
		}
		return
	}
	logger.LogGitHubAPICall("get_installation_id", true, "")

	// Create installation token with requested scopes
	token, err := CreateInstallationToken(ctx, githubClient.Apps, installationID, scopes)
	if err != nil {
		logger.LogGitHubAPICall("create_installation_token", false, err.Error())
		if strings.Contains(err.Error(), "insufficient permissions") ||
			strings.Contains(err.Error(), "fewer scopes") ||
			strings.Contains(err.Error(), "suspended") {
			logger.LogResponse(http.StatusForbidden, nil)
			writeError(w, http.StatusForbidden, err.Error(), nil)
		} else {
			logger.LogResponse(http.StatusServiceUnavailable, nil)
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("GitHub API error: %v", err), nil)
		}
		return
	}
	logger.LogGitHubAPICall("create_installation_token", true, "")

	// Build response
	response := TokenResponse{
		Token:     token.GetToken(),
		ExpiresAt: token.GetExpiresAt().Format(time.RFC3339),
		Scopes:    scopes,
	}

	logger.LogResponse(http.StatusOK, scopes)
	writeJSON(w, http.StatusOK, response)
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error: failed to encode response"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(jsonBytes)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, statusCode int, message string, details map[string]interface{}) {
	response := ErrorResponse{
		Error:   message,
		Details: details,
	}
	writeJSON(w, statusCode, response)
}
