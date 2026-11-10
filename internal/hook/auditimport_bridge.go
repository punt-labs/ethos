package hook

import "github.com/punt-labs/ethos/v4/internal/auditimport"

// The legacy audit reader and importer live in internal/auditimport (a leaf
// both this package and internal/autorepair import). These names keep every
// caller and test in this package reading as before.

type (
	rawAuditLine = auditimport.Line
	migrateNote  = auditimport.Note
)

const (
	stagedSuffix = auditimport.StagedSuffix
	kindMarker   = auditimport.KindMarker
)

var (
	decodeAuditLine           = auditimport.DecodeLine
	enumerateLegacyAuditFiles = auditimport.EnumerateLegacyFiles
	noticeMigratedFile        = auditimport.NoticeMigratedFile
	readLegacyAuditLines      = auditimport.ReadLegacyLines
	redactLegacyLines         = auditimport.RedactLegacyLines
)
