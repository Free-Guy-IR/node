package singbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

func (s *SingBox) startupLogTailSize() int {
	if s.cfg != nil && s.cfg.StartupLogTailSize > 0 {
		return s.cfg.StartupLogTailSize
	}
	return 200
}

func (s *SingBox) extractStartupError() error {
	failure := s.core.LatestStartupFailure()
	if failure == "" {
		return nil
	}
	return fmt.Errorf("failed to start sing-box: %s", failure)
}

func (s *SingBox) startupErrorWithTail(reason string) error {
	failure := s.core.LatestStartupFailure()
	if failure != "" {
		return fmt.Errorf("failed to start sing-box: %s", failure)
	}

	tail := s.core.StartupLogTail(s.startupLogTailSize())
	if len(tail) == 0 {
		return errors.New(reason)
	}

	return fmt.Errorf("%s; no fatal sing-box startup log was detected. Recent sing-box logs:\n%s", reason, strings.Join(tail, "\n"))
}

// checkStatus polls GetSysStats (a live round-trip through the v2ray_api
// StatsService) until it succeeds or a fatal startup error is observed in the
// captured logs, mirroring xray's Xray.checkXrayStatus.
func (s *SingBox) checkStatus(baseCtx context.Context) error {
	apiTicker := time.NewTicker(1 * time.Second)
	defer apiTicker.Stop()
	errorTicker := time.NewTicker(2 * time.Second)
	defer errorTicker.Stop()

	for {
		select {
		case <-baseCtx.Done():
			return errors.New("context cancelled")

		case <-errorTicker.C:
			if err := s.extractStartupError(); err != nil {
				return err
			}

		case <-apiTicker.C:
			ctx, cancel := context.WithTimeout(baseCtx, 1*time.Second)
			_, err := s.GetSysStats(ctx)
			cancel()

			if err == nil {
				return nil
			}

			if err := s.extractStartupError(); err != nil {
				return err
			}

			if !s.core.Started() {
				reason := "sing-box process stopped before API became ready"
				if s.core.Stopping() {
					reason = "sing-box startup was interrupted by a stop/restart request before API became ready"
				}
				return s.startupErrorWithTail(reason)
			}
		}
	}
}

// checkHealth periodically verifies the v2ray_api is still responsive and
// restarts sing-box after repeated consecutive failures, mirroring xray's
// Xray.checkXrayHealth.
func (s *SingBox) checkHealth(baseCtx context.Context) {
	consecutiveFailures := 0
	maxFailures := 10
	checkInterval := 2 * time.Second

	restart := func(reason string) {
		log.Println(reason)
		if tail := s.core.StartupLogTail(10); len(tail) > 0 {
			log.Printf("last %d sing-box log lines before restart:\n%s", len(tail), strings.Join(tail, "\n"))
		}
		if err := s.Restart(); err != nil {
			log.Println(err.Error())
		} else {
			log.Println("sing-box restarted")
			consecutiveFailures = 0
		}
	}

	for {
		select {
		case <-baseCtx.Done():
			return
		default:
			if s.core.Restarting() {
				consecutiveFailures = 0
				time.Sleep(checkInterval)
				continue
			}

			ctx, cancel := context.WithTimeout(baseCtx, 3*time.Second)
			_, err := s.GetSysStats(ctx)
			cancel()

			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}

				consecutiveFailures++
				log.Printf("sing-box health check failure %d/%d: %v", consecutiveFailures, maxFailures, err)

				if !s.core.Started() {
					restart("sing-box process is not running, restarting...")
				} else if consecutiveFailures >= maxFailures {
					restart(fmt.Sprintf("sing-box health check failed %d times, restarting...", consecutiveFailures))
				}
			} else {
				consecutiveFailures = 0
			}
		}
		time.Sleep(checkInterval)
	}
}
