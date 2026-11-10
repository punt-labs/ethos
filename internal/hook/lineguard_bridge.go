package hook

import "github.com/punt-labs/ethos/v4/internal/lineguard"

// The audit line guard lives in internal/lineguard (a leaf both this package
// and internal/autorepair import). These names keep every caller and test in
// this package reading as before; new code uses lineguard directly.

type (
	auditEntry  = lineguard.Entry
	guardReport = lineguard.Report
	lineGuard   = lineguard.LineGuard
)

const (
	auditPathFormat    = lineguard.AuditPathFormat
	redactedPath       = lineguard.RedactedPath
	redactedKey        = lineguard.RedactedKey
	redactedValueToken = lineguard.RedactedValueToken
)

var (
	sweepPrompt            = lineguard.SweepPrompt
	ambiguousPath          = lineguard.AmbiguousPath
	bareToolName           = lineguard.BareToolName
	caseCollision          = lineguard.CaseCollision
	credentialPassLine     = lineguard.CredentialPassLine
	dedupeLine             = lineguard.DedupeLine
	extractToolInput       = lineguard.ExtractToolInput
	guardAuditLineFull     = lineguard.GuardAuditLineFull
	guardAuditLineNote     = lineguard.GuardAuditLineNote
	guardAuditLineWhy      = lineguard.GuardAuditLineWhy
	hashToolInput          = lineguard.HashToolInput
	isJSONObject           = lineguard.IsJSONObject
	joinRules              = lineguard.JoinRules
	missionLineGuard       = lineguard.MissionLineGuardFor
	printNotices           = lineguard.PrintNotices
	quoteUnlessLeak        = lineguard.QuoteUnlessLeak
	redactAbsolutePaths    = lineguard.RedactAbsolutePaths
	redactLegacyLine       = lineguard.RedactLegacyLine
	redactSensitiveContent = lineguard.RedactSensitiveContent
	redactToolInput        = lineguard.RedactToolInput
	redactToolInputQuiet   = lineguard.RedactToolInputQuiet
	remarshalled           = lineguard.Remarshalled
	sessionLineGuard       = lineguard.SessionLineGuardFor
	sweepString            = lineguard.SweepString
	toolInputPreview       = lineguard.ToolInputPreview
)
