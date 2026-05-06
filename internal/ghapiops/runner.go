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

func (u *Usage) Add(other Usage) {
	u.RESTRequests += other.RESTRequests
	u.GraphQLRequests += other.GraphQLRequests
	u.GraphQLCost += other.GraphQLCost
	u.Pages += other.Pages
	u.PollIterations += other.PollIterations
}

type Result[T any] struct {
	Value   T
	Backend Backend
	Usage   Usage
}

type FetchFunc[T any] func(context.Context) (T, Usage, error)

type FallbackPredicate func(error) bool

func FetchWithFallback[T any](ctx context.Context, order []Backend, fetchers map[Backend]FetchFunc[T], canFallback FallbackPredicate) (Result[T], error) {
	if len(order) == 0 {
		return Result[T]{}, errors.New("no backend selected")
	}

	var errs []error
	var totalUsage Usage
	for index, backend := range order {
		if err := ctx.Err(); err != nil {
			return Result[T]{Usage: totalUsage}, err
		}

		fetcher, ok := fetchers[backend]
		if !ok {
			wrappedErr := fmt.Errorf("backend unavailable: %s", backend)
			return Result[T]{Usage: totalUsage}, errors.Join(append(errs, wrappedErr)...)
		}

		value, usage, err := fetcher(ctx)
		totalUsage.Add(usage)
		if err == nil {
			return Result[T]{
				Value:   value,
				Backend: backend,
				Usage:   totalUsage,
			}, nil
		}

		wrappedErr := fmt.Errorf("%s backend: %w", backend, err)
		if index == len(order)-1 || len(order) == 1 || canFallback == nil || !canFallback(err) {
			return Result[T]{Usage: totalUsage}, errors.Join(append(errs, wrappedErr)...)
		}
		errs = append(errs, wrappedErr)
	}
	panic("unreachable: non-empty backend order must return from loop")
}
