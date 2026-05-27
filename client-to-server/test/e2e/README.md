# client-to-server e2e

The e2e suite requires Docker and a local NATS JetStream container.

```sh
cd client-to-server/test/e2e && docker compose up -d
cd ../.. && go test -tags=e2e ./test/e2e/... -v
cd test/e2e && docker compose down -v
```

