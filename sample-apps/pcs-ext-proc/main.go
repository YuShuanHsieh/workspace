package main

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
