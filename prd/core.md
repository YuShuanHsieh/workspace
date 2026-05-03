# Workspace Architecture Design

## High-Level Requirments

### User is Non-Developer User
1. End-user should be able to use the workspace to view and update cross-applications data in a single patform.
2. End-user should be able to interact with the apps following a unified flow. (for example, when they get a chat message from other users or they get a new task assigned, they can be notified from a inbox notification center, and they can interact with the app from the notification)
3. End-user can only view and use the data with the permission they have.

### User is Developer
1. The workspace is a platform service that allows APP developers to onboard their app to our workspace platform.
2. The workspace should provide a unified interface for developers to help them quickly build their app with our provided tool.
3. The workspace should provide a unified and convenient interface for developers to interact with the other apps.
4. The workspace should be able to monitor or audit all events or actions that are happening in the workspace.
5. When an app does something, it can notify other apps and let other apps to take actions based on the notification.

## Idea
1. Using NATS JetStreams as the communication layer between apps and web client to web server. The benifits are the workspace can monitoring the status of each app, and also can push data to the web client in real-time.
2. The core engine is an event driven architecture. When a user does something in one app, it will generate an event and send it to the NATS server. The NATS server will then forward the event to all the apps that are interested in that event. 
3. To make sure low latency, in a single app's namespace, the event will be sent directly from clients to app's backend server via JetStreams. The workspace only subscribe to the global namespace events.
4. The make sure the data correctness, when an application needs to cross-communicate with other's application, the event will first sent to the workspace's backend server for schema validation, and the workspace's backend server will then forward the event to the other's application's backend server via JetStreams.
5. All applications will not need to expose their domain backend directly to the internet, they can subscribe the events from JetStreams directly.
6. Becuase all user actions or app's event will be sent to the NATS server, we can use NATS JetStreams to provide audit log or monitoring service.


## Esitmations
- users: 200,000 users
- all requests per day (end-user): 6,000,000 requests
- Peak RPS: 500 RPS
- applications: 20
- events from application: 240,000,000 events
- event cannot lose, latency < 500ms
