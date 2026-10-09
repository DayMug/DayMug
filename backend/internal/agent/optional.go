package agent

// Optional Backend features are expressed as separate interfaces rather than
// as flags on Capabilities, because a caller that wants steering needs the
// method, not a boolean. The cost of that shape is that "can this backend
// steer?" was answerable only by writing a type assertion at each call site,
// so the answer could not be logged, passed around, or asserted in a test
// without repeating the assertion.
//
// The accessors below make the answer a value while keeping the interface as
// the single source of truth: nothing here is hand-maintained, so a bit can't
// drift away from what a backend actually implements the way a second
// hand-written matrix would. That distinction matters most for the failure it
// prevents — renaming SteerTurn stops satisfying TurnSteeringBackend with no
// compile error anywhere, and steering silently disappears. See
// TestShippedBackendsKeepTheirOptionalCapabilities, which pins the matrix for
// the adapters DayMug actually ships.

// OptionalCapabilities reports which Backend extensions a given backend
// implements. Unlike Capabilities — a matrix each adapter fills in by hand —
// these are derived from the backend itself.
type OptionalCapabilities struct {
	// Steering: input can be appended to an already-running turn.
	Steering bool
	// UserQuestions: a paused provider session can be resumed with answers.
	UserQuestions bool
}

// OptionalCapabilitiesOf derives b's optional feature set. A nil backend
// reports everything disabled, which is what a caller holding an
// unconfigured provider should see. A TransportSwitch reports the transport
// currently selected.
func OptionalCapabilitiesOf(b Backend) OptionalCapabilities {
	b = Resolve(b)
	if b == nil {
		return OptionalCapabilities{}
	}
	_, steering := b.(TurnSteeringBackend)
	_, questions := b.(UserQuestionBackend)
	return OptionalCapabilities{Steering: steering, UserQuestions: questions}
}

// SteererOf returns b as a TurnSteeringBackend when it can steer.
func SteererOf(b Backend) (TurnSteeringBackend, bool) {
	s, ok := Resolve(b).(TurnSteeringBackend)
	return s, ok
}

// QuestionResponderOf returns b as a UserQuestionBackend when it can accept
// answers to a provider-side question.
func QuestionResponderOf(b Backend) (UserQuestionBackend, bool) {
	q, ok := Resolve(b).(UserQuestionBackend)
	return q, ok
}
