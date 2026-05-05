package ghapiops

import (
	"context"
	"errors"
	"testing"
)

func TestFetchWithFallback(t *testing.T) {
	t.Parallel()

	t.Run("falls back on eligible errors", func(t *testing.T) {
		t.Parallel()

		rest := &stubFetchFunc[bool]{
			err:   errors.New("rate limit"),
			usage: Usage{RESTRequests: 1},
		}
		graphql := &stubFetchFunc[bool]{
			value: true,
			usage: Usage{GraphQLRequests: 1, GraphQLCost: 2},
		}

		result, err := FetchWithFallback(
			context.Background(),
			[]Backend{BackendREST, BackendGraphQL},
			map[Backend]FetchFunc[bool]{
				BackendREST:    rest.Fetch,
				BackendGraphQL: graphql.Fetch,
			},
			func(err error) bool { return err.Error() == "rate limit" },
		)
		if err != nil {
			t.Fatalf("FetchWithFallback() error = %v", err)
		}
		if !result.Value {
			t.Fatal("FetchWithFallback() did not return fallback value")
		}
		if result.Backend != BackendGraphQL {
			t.Fatalf("FetchWithFallback() backend = %q, want graphql", result.Backend)
		}
		if result.Usage.RESTRequests != 1 || result.Usage.GraphQLRequests != 1 || result.Usage.GraphQLCost != 2 {
			t.Fatalf("FetchWithFallback() usage = %#v, want aggregated usage", result.Usage)
		}
		if rest.calls != 1 || graphql.calls != 1 {
			t.Fatalf("FetchWithFallback() calls = rest:%d graphql:%d, want 1/1", rest.calls, graphql.calls)
		}
	})

	t.Run("does not fall back on ineligible errors", func(t *testing.T) {
		t.Parallel()

		rest := &stubFetchFunc[bool]{err: errors.New("not found")}
		graphql := &stubFetchFunc[bool]{value: true}

		_, err := FetchWithFallback(
			context.Background(),
			[]Backend{BackendREST, BackendGraphQL},
			map[Backend]FetchFunc[bool]{
				BackendREST:    rest.Fetch,
				BackendGraphQL: graphql.Fetch,
			},
			func(error) bool { return false },
		)
		if err == nil {
			t.Fatal("FetchWithFallback() error = nil, want ineligible error")
		}
		if !containsAny(err.Error(), "rest backend") {
			t.Fatalf("FetchWithFallback() error = %v, want backend context", err)
		}
		if graphql.calls != 0 {
			t.Fatalf("FetchWithFallback() graphql calls = %d, want 0", graphql.calls)
		}
	})

	t.Run("joins prior errors when fallback backend is unavailable", func(t *testing.T) {
		t.Parallel()

		rest := &stubFetchFunc[bool]{
			err:   errors.New("rate limit"),
			usage: Usage{RESTRequests: 1},
		}

		result, err := FetchWithFallback(
			context.Background(),
			[]Backend{BackendREST, BackendGraphQL},
			map[Backend]FetchFunc[bool]{
				BackendREST: rest.Fetch,
			},
			func(err error) bool { return err.Error() == "rate limit" },
		)
		if err == nil {
			t.Fatal("FetchWithFallback() error = nil, want joined error")
		}
		if !containsAny(err.Error(), "rest backend: rate limit", "backend unavailable: graphql") {
			t.Fatalf("FetchWithFallback() error = %v, want both backend errors", err)
		}
		if result.Usage.RESTRequests != 1 {
			t.Fatalf("FetchWithFallback() usage = %#v, want usage from attempted backend", result.Usage)
		}
	})

	t.Run("stops immediately when context is already canceled", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		rest := &stubFetchFunc[bool]{value: true}
		graphql := &stubFetchFunc[bool]{value: true}

		_, err := FetchWithFallback(
			ctx,
			[]Backend{BackendREST, BackendGraphQL},
			map[Backend]FetchFunc[bool]{
				BackendREST:    rest.Fetch,
				BackendGraphQL: graphql.Fetch,
			},
			func(error) bool { return true },
		)
		if err == nil {
			t.Fatal("FetchWithFallback() error = nil, want canceled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FetchWithFallback() error = %v, want context.Canceled", err)
		}
		if rest.calls != 0 || graphql.calls != 0 {
			t.Fatalf("FetchWithFallback() calls = rest:%d graphql:%d, want 0/0", rest.calls, graphql.calls)
		}
	})

	t.Run("errors when no backend is selected", func(t *testing.T) {
		t.Parallel()

		_, err := FetchWithFallback[int](context.Background(), nil, nil, nil)
		if err == nil {
			t.Fatal("FetchWithFallback() error = nil, want empty-order error")
		}
		if err.Error() != "no backend selected" {
			t.Fatalf("FetchWithFallback() error = %v, want no backend selected", err)
		}
	})
}

type stubFetchFunc[T any] struct {
	value T
	usage Usage
	err   error
	calls int
}

func (f *stubFetchFunc[T]) Fetch(context.Context) (T, Usage, error) {
	f.calls++
	if f.err != nil {
		var zero T
		return zero, f.usage, f.err
	}
	return f.value, f.usage, nil
}
