package usecases

import "strings"

// logReplacer strips the control characters CodeQL's log-injection check
// worries about from user-provided values before they reach a log line
// (fleet-standard sink fix; see task-interleaving internal/adapters/inbound/kafka/logsafe.go).
var logReplacer = strings.NewReplacer("\r", "", "\n", "")

// sanitizeLog makes a user-provided string safe to embed in a structured
// log entry by removing line-control characters.
func sanitizeLog(s string) string { return logReplacer.Replace(s) }
