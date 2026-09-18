package xray

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/pasarguard/node/backend"
)

var _ backend.DegradableBackend = (*Xray)(nil)

var routingRuleStartupFailureMarkers = []string{
	"failed to build routing configuration",
	"invalid router rule",
	"unsupported address for router",
	"invalid network mask for router",
	"neither outboundtag nor balancertag is specified in routing rule",
}

var routingContextMarkers = []string{
	"failed to build routing configuration",
	"invalid field rule",
	"invalid router rule",
}

var sharedAssetFailureMarkers = []string{
	"failed to open file:",
	"code not found in",
	"invalid external resource",
	"failed to load external sites",
	"failed to parse domain rule",
	"failed to load geosite",
	"failed to load geoip",
	"failed to load ips",
	"empty country name in rule",
	"empty filename or empty country in rule",
	"substr in dotless rule should not contain a dot",
}

var resourceExhaustionStartupFailureMarkers = []string{
	"out of memory",
	"cannot allocate memory",
	"signal: killed",
}

func containsAny(haystack string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}

	return false
}

func startupFailureIsRuleRelated(failure string) bool {
	lower := strings.ToLower(failure)

	if containsAny(lower, routingRuleStartupFailureMarkers) {
		return true
	}

	return containsAny(lower, sharedAssetFailureMarkers) && containsAny(lower, routingContextMarkers)
}

func startupFailureIsResourceExhaustion(failure string) bool {
	return containsAny(strings.ToLower(failure), resourceExhaustionStartupFailureMarkers)
}

func (x *Xray) StartupDegradation() backend.StartupDegradation {
	x.mu.RLock()
	defer x.mu.RUnlock()

	return backend.StartupDegradation{
		FilterRulesStripped: x.degradation.FilterRulesStripped,
		Reason:              x.degradation.Reason,
		StrippedRuleTags:    append([]string(nil), x.degradation.StrippedRuleTags...),
	}
}

func (x *Xray) setStartupDegradation(degradation backend.StartupDegradation) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.degradation = degradation
}

func (x *Xray) announce(message string) {
	log.Println(message)
	if x.core != nil {
		x.core.recordProcessLog(message)
	}
}

type coreStartFunc func(context.Context, *Config) error

func (x *Xray) startCoreAndWait(ctx context.Context, config *Config) error {
	debug := false
	if x.cfg != nil {
		debug = x.cfg.Debug
	}

	if err := x.core.Start(config, debug); err != nil {
		return err
	}

	return x.checkXrayStatus(ctx)
}

func (x *Xray) startCoreWithFilterFallback(ctx context.Context, config *Config, start coreStartFunc, stop func()) error {
	x.setConfig(config)

	startErr := start(ctx, config)
	if startErr == nil {
		return nil
	}

	tags := contentFilterRuleTags(config)
	if len(tags) == 0 {
		return startErr
	}

	exhausted := startupFailureIsResourceExhaustion(startErr.Error())
	if !startupFailureIsRuleRelated(startErr.Error()) && !exhausted {
		return startErr
	}

	if exhausted {
		if stop != nil {
			stop()
		}

		x.announce(fmt.Sprintf("xray was starved of resources at startup, retrying unchanged before blaming any filter: %v", startErr))

		retryErr := start(ctx, config)
		if retryErr == nil {
			return nil
		}

		if !startupFailureIsResourceExhaustion(retryErr.Error()) && !startupFailureIsRuleRelated(retryErr.Error()) {
			x.announce(fmt.Sprintf("the unchanged retry failed for a reason unrelated to any filter, leaving the rules in place: %v", retryErr))
			return retryErr
		}

		startErr = retryErr
	}

	x.announce(fmt.Sprintf("xray failed to start with %d content-filter rule(s) in its routing section: %v", len(tags), startErr))

	stripped, err := config.Clone()
	if err != nil {
		log.Printf("cannot retry xray without the content-filter rules: %v", err)
		return startErr
	}

	removed := stripContentFilterRules(stripped)
	if len(removed) == 0 {
		return startErr
	}

	if stop != nil {
		stop()
	}

	x.announce(fmt.Sprintf("retrying xray startup without the content-filter rules: %s", strings.Join(removed, ", ")))

	if retryErr := start(ctx, stripped); retryErr != nil {
		x.announce(fmt.Sprintf("xray still failed to start after the content-filter rules were removed: %v", retryErr))
		return startErr
	}

	x.setConfig(stripped)
	x.setStartupDegradation(backend.StartupDegradation{
		FilterRulesStripped: true,
		Reason:              startErr.Error(),
		StrippedRuleTags:    removed,
	})

	x.announce(fmt.Sprintf("xray started DEGRADED: %d content-filter rule(s) removed (%s) after startup failure: %v",
		len(removed), strings.Join(removed, ", "), startErr))

	return nil
}
