package watchers

import (
	"time"

	"github.com/charmbracelet/log"
	"github.com/robfig/cron/v3"
)

type CronWatcher struct {
	scheduler *cron.Cron
	done      chan struct{}
}

func NewCronWatcher() *CronWatcher {
	return &CronWatcher{
		scheduler: cron.New(cron.WithParser(cron.NewParser(
			cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
		))),
		done: make(chan struct{}),
	}
}

func (cw *CronWatcher) Name() string { return "cron" }

func (cw *CronWatcher) AddSchedule(schedule string, skillName string, events chan<- Event) error {
	_, err := cw.scheduler.AddFunc(schedule, func() {
		events <- Event{
			Source: "cron",
			Type:   schedule,
			Payload: map[string]any{
				"skill":    skillName,
				"schedule": schedule,
			},
			Timestamp: time.Now(),
		}
	})
	if err != nil {
		return err
	}

	log.Debug("cron schedule added", "schedule", schedule, "skill", skillName)
	return nil
}

func (cw *CronWatcher) Start(events chan<- Event) error {
	cw.scheduler.Start()
	log.Info("cron watcher started")
	return nil
}

func (cw *CronWatcher) Stop() error {
	ctx := cw.scheduler.Stop()
	<-ctx.Done()
	return nil
}
