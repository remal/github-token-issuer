package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// RequestLogger provides conditional logging that only emits logs
// when the service is invoked via a Cloud Run tag URL.
type RequestLogger struct {
	enabled   bool
	startTime time.Time
	repo      string
	scopes    int
}

// NewRequestLogger creates a logger that only logs if the request came through a tag URL.
// Tag URLs have the format: https://{tag}---{service}-{hash}-{region}.a.run.app
func NewRequestLogger(r *http.Request) *RequestLogger {
	enabled := strings.Contains(r.Host, "---")
	return &RequestLogger{
		enabled:   enabled,
		startTime: time.Now(),
	}
}

// SetRepository sets the repository for logging context.
func (l *RequestLogger) SetRepository(repo string) {
	l.repo = repo
}

// SetScopesCount sets the number of scopes for logging context.
func (l *RequestLogger) SetScopesCount(count int) {
	l.scopes = count
}

// LogRequest logs the incoming request details.
func (l *RequestLogger) LogRequest(scopes map[string]string) {
	if !l.enabled {
		return
	}

	scopeNames := make([]string, 0, len(scopes))
	for name := range scopes {
		scopeNames = append(scopeNames, name)
	}

	l.logJSON(map[string]interface{}{
		"event":  "request_received",
		"repo":   l.repo,
		"scopes": scopeNames,
	})
}

// LogValidationError logs a validation failure.
func (l *RequestLogger) LogValidationError(errorType string, detail string) {
	if !l.enabled {
		return
	}

	l.logJSON(map[string]interface{}{
		"event":      "validation_failed",
		"repo":       l.repo,
		"error_type": errorType,
		"detail":     detail,
	})
}

// LogGitHubAPICall logs the outcome of a GitHub API call.
func (l *RequestLogger) LogGitHubAPICall(operation string, success bool, errorMsg string) {
	if !l.enabled {
		return
	}

	entry := map[string]interface{}{
		"event":     "github_api",
		"repo":      l.repo,
		"operation": operation,
		"success":   success,
	}
	if errorMsg != "" {
		entry["error"] = errorMsg
	}

	l.logJSON(entry)
}

// LogResponse logs the final response.
func (l *RequestLogger) LogResponse(statusCode int, grantedScopes map[string]string) {
	if !l.enabled {
		return
	}

	entry := map[string]interface{}{
		"event":       "response_sent",
		"repo":        l.repo,
		"status":      statusCode,
		"duration_ms": time.Since(l.startTime).Milliseconds(),
	}

	if grantedScopes != nil {
		scopeNames := make([]string, 0, len(grantedScopes))
		for name := range grantedScopes {
			scopeNames = append(scopeNames, name)
		}
		entry["scopes_granted"] = scopeNames
	}

	l.logJSON(entry)
}

// logJSON outputs a structured JSON log entry.
func (l *RequestLogger) logJSON(entry map[string]interface{}) {
	entry["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	log.Println(string(data))
}
