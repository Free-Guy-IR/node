package xray

import (
	"context"
	"errors"
	"log"
	"time"
)

func (x *Xray) checkXrayStatus(baseCtx context.Context) error {
	consecutiveFailures := 0
	maxFailures := 10 // Allow a few failures before restarting

	for {
		select {
		case <-baseCtx.Done():
			return errors.New("canceled")
		default:
			ctx, cancel := context.WithTimeout(baseCtx, time.Second*1)
			_, err := x.GetSysStats(ctx)
			cancel()

			if err == nil {
				return nil
			} else {
				consecutiveFailures++
				if consecutiveFailures >= maxFailures {
					return err
				}
			}
		}
		time.Sleep(time.Millisecond * 500)
	}
}

func (x *Xray) checkXrayHealth(baseCtx context.Context) {
	consecutiveFailures := 0
	maxFailures := 3 // Allow a few failures before restarting

	for {
		select {
		case <-baseCtx.Done():
			return
		default:
			ctx, cancel := context.WithTimeout(baseCtx, time.Second*3)
			_, err := x.GetSysStats(ctx)
			cancel()

			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}

				consecutiveFailures++
				// Only restart after multiple consecutive failures
				if consecutiveFailures >= maxFailures {
					log.Printf("xray health check failed %d times, restarting...", consecutiveFailures)
					if err = x.Restart(); err != nil {
						log.Println(err.Error())
					} else {
						log.Println("xray restarted")
						consecutiveFailures = 0 // Reset counter after restart
					}
				}
			} else {
				consecutiveFailures = 0 // Reset on success
			}
		}
		time.Sleep(time.Second * 5)
	}
}
