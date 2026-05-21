package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type ruleKey struct {
	user, objectID, objectType, permission string
}

var rules = map[ruleKey]bool{
	{"alice@workspace.test", "doc-1", "document", "edit"}: true,
	{"alice@workspace.test", "doc-1", "document", "read"}: true,
	{"alice@workspace.test", "doc-2", "document", "edit"}: false,
	{"bob@workspace.test", "doc-1", "document", "read"}:   true,
	{"bob@workspace.test", "doc-1", "document", "edit"}:   false,
}

// decide returns true iff (user, objectID, objectType, permission) is in the
// allow-list. Default is deny (false) — both for explicit deny rules and for
// completely unknown combinations.
func decide(user, objectID, objectType, permission string) bool {
	return rules[ruleKey{user, objectID, objectType, permission}]
}

type checkRequest struct {
	ObjectID   string `json:"objectId"`
	ObjectType string `json:"objectType"`
	Permission string `json:"permission"`
}

type checkResponse struct {
	Allowed bool `json:"allowed"`
}

func checkHandler(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	allowed := decide(user, req.ObjectID, req.ObjectType, req.Permission)
	slog.Info("decision",
		"ts", time.Now().UTC().Format(time.RFC3339Nano),
		"user", user,
		"obj", req.ObjectID,
		"type", req.ObjectType,
		"perm", req.Permission,
		"decision", map[bool]string{true: "allow", false: "deny"}[allowed],
	)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(checkResponse{Allowed: allowed})
}
