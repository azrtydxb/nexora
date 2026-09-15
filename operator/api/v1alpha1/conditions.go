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
	// ReasonJoinTokenRevokeFailed: a join token this CR owns could not be revoked. No new token is
	// created while that is true, so a failing revoke cannot multiply tokens.
	ReasonJoinTokenRevokeFailed = "JoinTokenRevokeFailed"
	// ReasonJoinTokenLimit: this CR already owns the maximum number of active join tokens.
	ReasonJoinTokenLimit = "JoinTokenLimit"
	ReasonReconciled     = "Reconciled"

	FinalizerEngineGroup = "nexora.io/engine-group"
	LabelInstallation    = "nexora.io/installation"
	FieldManager         = "nexora-operator"
)
