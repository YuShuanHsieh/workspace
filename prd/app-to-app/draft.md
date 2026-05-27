# Goal
Enable applications can publish events to another applications, so that A application can use B application's public functions to do something.

# Prerequisites
1. All applications must be registered in APP registry
2. A public function means application reigster their avaliable public events to event marketplace. The event information includes object schema (the request payload) and subject.
3. The event must be validated before publishing(publish-side) to the target application (include app registry and event marketplace)
4. The consumer-side must be validate the events are from avaliable applications (registed in APP registry)
5. The events need to contain some context for tracing and application can know the event is from who and from which previous event

# Limit
1. The event format uses cloudEvent 
2. Tech stack: event bus use NATS, database use mongoDB
