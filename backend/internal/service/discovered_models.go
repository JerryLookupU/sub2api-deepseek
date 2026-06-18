package service

import (
	"hash/fnv"
	"reflect"
	"sort"
	"strings"
	"time"
)

// extra JSONB keys for upstream-discovered models. Stored on account.extra and
// propagated to the routing hot path automatically because UpdateExtra enqueues
// a scheduler-outbox rebuild for any non-neutral key (see account_repo.go:
// shouldEnqueueSchedulerOutboxForExtraUpdates).
const (
	discoveredModelsExtraKey        = "discovered_models"
	discoveredModelsSyncedAtExtraKey = "discovered_models_synced_at"
)

// GetDiscoveredModels returns the upstream-discovered model set cached on the
// account's extra map, backed by an in-struct hot-path cache that mirrors
// GetModelMapping. Returns nil when discovery has not run or produced no models.
func (a *Account) GetDiscoveredModels() map[string]struct{} {
	if a == nil || a.Extra == nil {
		return nil
	}
	rawSlice, _ := a.Extra[discoveredModelsExtraKey].([]any)
	extraPtr := mapPtr(a.Extra)
	rawPtr := sliceAnyPtr(rawSlice)
	rawLen := len(rawSlice)
	rawSig := uint64(0)
	rawSigReady := false

	if a.discoveredModelsCacheReady &&
		a.discoveredModelsCacheExtraPtr == extraPtr &&
		a.discoveredModelsCacheRawPtr == rawPtr &&
		a.discoveredModelsCacheRawLen == rawLen {
		rawSig = discoveredModelsSignature(rawSlice)
		rawSigReady = true
		if a.discoveredModelsCacheRawSig == rawSig {
			return a.discoveredModelsCache
		}
	}

	set := make(map[string]struct{}, rawLen)
	for _, item := range rawSlice {
		s, ok := item.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		set[s] = struct{}{}
	}

	if len(set) == 0 {
		// Don't cache an empty set; let callers fall through to legacy behavior.
		return nil
	}

	if !rawSigReady {
		rawSig = discoveredModelsSignature(rawSlice)
	}

	a.discoveredModelsCache = set
	a.discoveredModelsCacheReady = true
	a.discoveredModelsCacheExtraPtr = extraPtr
	a.discoveredModelsCacheRawPtr = rawPtr
	a.discoveredModelsCacheRawLen = rawLen
	a.discoveredModelsCacheRawSig = rawSig
	return set
}

// HasDiscoveredModels reports whether upstream discovery has produced a
// non-empty model set for this account.
func (a *Account) HasDiscoveredModels() bool {
	return a != nil && len(a.GetDiscoveredModels()) > 0
}

// GetDiscoveredModelsSyncedAt returns the last successful discovery timestamp,
// or the zero time if discovery has never run.
func (a *Account) GetDiscoveredModelsSyncedAt() time.Time {
	if a == nil || a.Extra == nil {
		return time.Time{}
	}
	switch v := a.Extra[discoveredModelsSyncedAtExtraKey].(type) {
	case time.Time:
		return v
	case string:
		if v == "" {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return parsed
		}
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func sliceAnyPtr(s []any) uintptr {
	if len(s) == 0 {
		return 0
	}
	return reflect.ValueOf(s).Pointer()
}

func discoveredModelsSignature(items []any) uint64 {
	if len(items) == 0 {
		return 0
	}
	sorted := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			sorted = append(sorted, s)
		}
	}
	sort.Strings(sorted)
	h := fnv.New64a()
	for _, s := range sorted {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0xff})
	}
	return h.Sum64()
}
