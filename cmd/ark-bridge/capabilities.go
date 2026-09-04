package main

// The bridge<->SDK compatibility contract. The SDK REQUIRES a set of capabilities and a
// compatible protocol version; a stale or mismatched bridge that does not advertise them makes
// the SDK fail loudly rather than silently running with weaker (or missing) supervision
// guarantees. Capabilities are additive and named, so the contract is robust to a plain version
// bump: the SDK checks for the exact behaviours it depends on.
const protocolVersion = 1

// capabilities lists the hardened behaviours this bridge implements. Bump/extend when a new
// guarantee the SDK may depend on is added; never remove one without a protocol bump.
var capabilities = []string{
	"supervision",           // fail-closed deterministic supervision kernel
	"action_binding",        // executed_action must match the authorized action
	"consume",               // pre-execution consume gate (TOCTOU freshness re-check)
	"authorization_id",      // stable durable authorization id (cross-restart / cross-instance)
	"namespace",             // tenant/namespace isolation + binding
	"transaction_isolation", // retry state isolated per (transaction, constraint)
	"freshness",             // observed_at/expires_at + bounded clock skew
	"durable_authz",         // durable ISSUED->CONSUMED->COMPLETED store (ARK_AUTHZ_DIR)
	"trusted_provider",      // trusted-evidence-plane enforcement
	"status_reconcile",      // status command + AMBIGUOUS reconciliation
	"audit",                 // structured redacted audit provenance
}

// helloReply is the capability advertisement returned by the `hello` command and embedded in
// every session `start` reply, so the SDK can verify compatibility before it relies on any
// guarantee.
func helloReply() map[string]any {
	return map[string]any{
		"ok":               true,
		"protocol_version": protocolVersion,
		"capabilities":     capabilities,
		"bridge":           "ark-bridge",
	}
}
