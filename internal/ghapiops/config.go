package ghapiops

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const maxInt64 = math.MaxInt64

type Backend string

const (
	BackendAuto    Backend = "auto"
	BackendRandom  Backend = "random"
	BackendGraphQL Backend = "graphql"
	BackendREST    Backend = "rest"
)

type Config struct {
	Backend           Backend
	RESTWeight        int
	GraphQLWeight     int
	AutoAdjustWeights bool
}

func NewConfig(backend string, restWeight, graphqlWeight int, autoAdjustWeights bool) Config {
	return Config{
		Backend:           Backend(strings.ToLower(backend)),
		RESTWeight:        restWeight,
		GraphQLWeight:     graphqlWeight,
		AutoAdjustWeights: autoAdjustWeights,
	}
}

func (c Config) IsAdaptive() bool {
	return c.Backend == BackendAuto || c.Backend == BackendRandom
}

func ValidateConfig(config Config) error {
	switch config.Backend {
	case BackendAuto, BackendRandom, BackendGraphQL, BackendREST:
	default:
		return fmt.Errorf("backend must be one of auto, random, rest, graphql: %q", config.Backend)
	}
	if config.RESTWeight < 0 {
		return fmt.Errorf("rest-weight must be non-negative: %d", config.RESTWeight)
	}
	if config.GraphQLWeight < 0 {
		return fmt.Errorf("graphql-weight must be non-negative: %d", config.GraphQLWeight)
	}
	if config.IsAdaptive() && config.RESTWeight == 0 && config.GraphQLWeight == 0 {
		return errors.New("adaptive polling requires rest-weight or graphql-weight to be positive")
	}
	return nil
}

func SelectBackends(config Config, limits *RateLimitSnapshot, randomInt63n func(int64) int64) ([]Backend, error) {
	switch config.Backend {
	case BackendGraphQL:
		return []Backend{BackendGraphQL}, nil
	case BackendREST:
		return []Backend{BackendREST}, nil
	case BackendAuto:
		primary := preferredAutoBackend(config, limits)
		return backendOrder(primary), nil
	case BackendRandom:
		restWeight, graphqlWeight := effectiveWeights(config, limits)
		primary, err := chooseWeightedBackend(restWeight, graphqlWeight, randomInt63n)
		if err != nil {
			return nil, err
		}
		return backendOrder(primary), nil
	default:
		return nil, fmt.Errorf("unsupported polling backend: %q", config.Backend)
	}
}

func preferredAutoBackend(config Config, limits *RateLimitSnapshot) Backend {
	restWeight, graphqlWeight := effectiveWeights(config, limits)
	if graphqlWeight > restWeight {
		return BackendGraphQL
	}
	return BackendREST
}

func effectiveWeights(config Config, limits *RateLimitSnapshot) (restWeight, graphqlWeight int64) {
	restWeight = int64(config.RESTWeight)
	graphqlWeight = int64(config.GraphQLWeight)
	if !config.AutoAdjustWeights || limits == nil {
		return restWeight, graphqlWeight
	}

	adjustedREST := restWeight
	adjustedGraphQL := graphqlWeight
	if adjustedREST > 0 {
		adjustedREST = scaleWeight(adjustedREST, max(limits.CoreRemaining, 0), 1)
	}
	if adjustedGraphQL > 0 {
		adjustedGraphQL = scaleWeight(adjustedGraphQL, max(limits.GraphQLRemaining, 0), 1)
	}
	if adjustedREST == 0 && adjustedGraphQL == 0 {
		return restWeight, graphqlWeight
	}
	return adjustedREST, adjustedGraphQL
}

func scaleWeight(weight int64, remaining int, requestCost int64) int64 {
	if weight <= 0 || remaining <= 0 {
		return 0
	}
	scaled := saturatingMul(weight, int64(remaining))
	if requestCost == 1 {
		return scaled
	}
	return scaled / requestCost
}

func chooseWeightedBackend(restWeight, graphqlWeight int64, randomInt63n func(int64) int64) (Backend, error) {
	restWeight, graphqlWeight = normalizeWeightedPair(restWeight, graphqlWeight)
	total := restWeight + graphqlWeight
	if total <= 0 {
		return "", errors.New("adaptive polling requires rest-weight or graphql-weight to be positive")
	}
	if randomInt63n(total) < restWeight {
		return BackendREST, nil
	}
	return BackendGraphQL, nil
}

func saturatingMul(values ...int64) int64 {
	result := int64(1)
	for _, value := range values {
		if value == 0 {
			return 0
		}
		if result > maxInt64/value {
			return maxInt64
		}
		result *= value
	}
	return result
}

func normalizeWeightedPair(left, right int64) (int64, int64) {
	for left > maxInt64-right {
		if left > 1 {
			left = left/2 + left%2
		}
		if right > 1 {
			right = right/2 + right%2
		}
	}
	return left, right
}

func backendOrder(primary Backend) []Backend {
	if primary == BackendGraphQL {
		return []Backend{BackendGraphQL, BackendREST}
	}
	return []Backend{BackendREST, BackendGraphQL}
}
