package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/events/task-created", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"processed":      true,
			"eventId":        r.Header.Get("ce-id"),
			"idempotencyKey": r.Header.Get("Idempotency-Key"),
			"actorId":        r.Header.Get("X-Workspace-Actor-Id"),
			"tenantId":       r.Header.Get("X-Workspace-Tenant-Id"),
		})
	})
	if err := http.ListenAndServe("127.0.0.1:8080", nil); err != nil {
		log.Fatal(err)
	}
}
