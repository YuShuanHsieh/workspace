#!/usr/bin/env bash
set -euo pipefail

nats --server nats://127.0.0.1:4222 pub t.tenant-a.app.task.event.created '{
  "specversion": "1.0",
  "id": "evt-example-1",
  "source": "workspace/task",
  "type": "com.workspace.task.created",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1",
    "X-Workspace-Tenant-Id": "tenant-a"
  },
  "data": {"taskId": "task-1"}
}'

