package gitstore

// OperationError preserves the stage at which a source operation failed.
// Callers need not infer execution state from translated presentation text.
type OperationError struct {
	Stage string
	Err   error
}

func (e *OperationError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *OperationError) Unwrap() error { return e.Err }
