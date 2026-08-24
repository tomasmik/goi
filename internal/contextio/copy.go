package contextio

import (
	"context"
	"io"
)

func Copy(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	return io.CopyBuffer(
		writer{ctx: ctx, destination: destination},
		reader{ctx: ctx, source: source},
		make([]byte, 128<<10),
	)
}

type reader struct {
	ctx    context.Context
	source io.Reader
}

func (r reader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.source.Read(buffer)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return count, contextErr
	}
	return count, err
}

type writer struct {
	ctx         context.Context
	destination io.Writer
}

func (w writer) Write(buffer []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := w.destination.Write(buffer)
	if contextErr := w.ctx.Err(); contextErr != nil {
		return count, contextErr
	}
	return count, err
}
