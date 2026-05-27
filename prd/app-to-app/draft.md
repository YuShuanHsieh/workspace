# Goal
Enable an application to publish events to other applications so that application A can use application B's public functions.

# Prerequisites
1. All applications must be registered in APP registry
2. A public function means an application registers its available public events to event marketplace. The event information includes object schema (the request payload) and subject.
3. The event must be validated before publishing(publish-side) to the target application (include app registry and event marketplace)
4. The consumer-side must validate that events are from available applications (registered in APP registry)
5. The events need to contain some context for tracing so an application can know who the event is from and which previous event it came from

# Limit
1. The event format uses cloudEvent 
2. Tech stack: event bus use NATS, database use mongoDB
