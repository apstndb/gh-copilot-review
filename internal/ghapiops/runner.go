package ghapiops

import (
	"context"
	"errors"
	"fmt"
)

type Usage struct {
	RESTRequests    int
	GraphQLRequests int
	GraphQLCost     int
	Pages           int
	PollIterations  int
}

type Result[T any] struct {
	Value   T
	Backend Backend
	Usage   Usage
}

type FetchFunc[T any] func(context.Context) (T, Usage, error)

type FallbackPredicate func(error) bool

func FetchWithFallback[T any](ctx context.Context, order []Backend, fetchers map[Backend]FetchFunc[T], canFallback FallbackPredicate) (Result[T], error) {
	var errs []error
	for index, backend := range order {
		fetcher, ok := fetchers[backend]
		if !ok {
			return Result[T]{}, fmt.Errorf("backend unavailable: %s", backend)
		}

		value, usage, err := fetcher(ctx)
		if err == nil {
			return Result[T]{
				Value:   value,
				Backend: backend,
				Usage:   usage,
			}, nil
		}

		wrappedErr := fmt.Errorf("%s backend: %w", backend, err)
		if index == len(order)-1 || len(order) == 1 || canFallback == nil || !canFallback(err) {
			return Result[T]{}, errors.Join(append(errs, wrappedErr)...)
		}
		errs = append(errs, wrappedErr)
	}
	return Result[T]{}, errors.New("no backend selected")
}
