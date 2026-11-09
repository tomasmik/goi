package studyerror

import "fmt"

type Error string

func (err Error) Error() string {
	return string(err)
}

func (err Error) UserMessage() string {
	return string(err)
}

func Errorf(format string, args ...any) error {
	return Error(fmt.Sprintf(format, args...))
}
