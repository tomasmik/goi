package imports

import "errors"

var (
	errInvalidPackage = errors.New("invalid Anki package")
	errInvalidMapping = errors.New("invalid Anki field mapping")
	errRunUnavailable = errors.New("Anki import run is unavailable")
)

type classifiedError struct {
	kind    error
	message string
	cause   error
}

func (err classifiedError) Error() string {
	return err.message
}

func (err classifiedError) UserMessage() string {
	return err.message
}

func (err classifiedError) Is(target error) bool {
	return target == err.kind
}

func (err classifiedError) Unwrap() error {
	return err.cause
}

func invalidPackage(message string, cause error) error {
	return classifiedError{kind: errInvalidPackage, message: message, cause: cause}
}

func invalidMapping(message string) error {
	return classifiedError{kind: errInvalidMapping, message: message}
}

func unavailableRun(message string) error {
	return classifiedError{kind: errRunUnavailable, message: message}
}
