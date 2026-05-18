.PHONY: envoy.yaml up down clean

envoy.yaml: routes.yaml ../../cmd/validate-routes/main.go
	cd ../.. && go run ./cmd/validate-routes translate test/e2e/routes.yaml \
		-o test/e2e/envoy.yaml \
		--sidecar-host sidecar --sidecar-port 50051 \
		--backend-host fake-backend --backend-port 8080

up: envoy.yaml
	docker compose -f docker-compose.yaml up --build -d

down:
	docker compose -f docker-compose.yaml down -v

clean:
	rm -f envoy.yaml
