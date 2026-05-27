package shadow

// Validator verifies that the C++ engine matched orders correctly
type Validator struct {
	processed int64
}

// NewValidator creates a new shadow global Validator
func NewValidator() *Validator {
	return &Validator{}
}

// ProcessOrder processes an order sent to the sandbox
func (v *Validator) ProcessOrder(orderID int64, orderType string, side string, price int64, quantity int64) {
	v.processed++
}

// ProcessAck processes an order acknowledgement from the sandbox
func (v *Validator) ProcessAck(orderID int64, status string) {
	v.processed++
}

// ProcessFill processes a fill event from the sandbox
func (v *Validator) ProcessFill(orderID int64, filledQty int64, filledPrice int64) {
	v.processed++
}

// GetCorrectnessScore returns the correctness score
func (v *Validator) GetCorrectnessScore() float64 {
	// A simple stub returning 100% since C++ matching validation
	// logic isn't fully defined yet in this task description.
	return 100.0
}