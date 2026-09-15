package v1alpha1

// Condition types, reasons and fixed identifiers shared by both controllers.
const (
	ConditionRendered        = "Rendered"
	ConditionDatabaseReady   = "DatabaseReady"
	ConditionManagementReady = "ManagementReady"
	ConditionSetupRequired   = "SetupRequired"
	ConditionEnginesReady    = "EnginesReady"
	ConditionReady           = "Ready"
	ConditionSynced          = "Synced"
	ConditionJoinTokenReady  = "JoinTokenReady"

	ReasonRenderFailed          = "RenderFailed"
	ReasonImageTagRequired      = "ImageTagRequired"
	ReasonSecretIncomplete      = "SecretIncomplete"
	ReasonForeignNamespace      = "ForeignNamespace"
	ReasonJoinTokenPending      = "JoinTokenPending"
	ReasonRollingUpdate         = "RollingUpdate"
	ReasonUnavailable           = "Unavailable"
	ReasonManagementUnavailable = "ManagementUnavailable"
	ReasonUnauthorized          = "Unauthorized"
	ReasonConflict              = "Conflict"
	ReasonDuplicateGroupName    = "DuplicateGroupName"
	ReasonDeletionBlocked       = "DeletionBlocked"
	ReasonReconciled            = "Reconciled"

	FinalizerEngineGroup = "nexora.io/engine-group"
	LabelInstallation    = "nexora.io/installation"
	FieldManager         = "nexora-operator"
)
