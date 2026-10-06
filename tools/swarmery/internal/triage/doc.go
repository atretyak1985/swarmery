// Package triage runs the inbox triage agent: recorded, single-flight
// background runs that collect waiting inbox items from registered Sources,
// ask a Judge (a headless `claude -p`, or a Source's own rule-based Decider)
// for one value per item part, and pass every resulting write through the Go
// policy table in policy.go before a Source may apply it.
//
// Nothing a model answers is trusted on its own: a value outside the part's
// allowed set, a kind the policy does not name, or one of the denied kinds
// (approval, proposal, alert) is stored as a 'rejected' verdict and never
// applied. Every applied verdict keeps the Source's prior state so the operator
// can undo it.
package triage
